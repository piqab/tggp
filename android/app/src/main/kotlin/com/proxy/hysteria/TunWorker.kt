package com.proxy.hysteria

import android.util.Log
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import java.io.FileDescriptor
import java.io.FileInputStream
import java.io.FileOutputStream
import java.io.OutputStream
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.Socket
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.concurrent.ConcurrentHashMap

/**
 * Reads raw IPv4 packets from the TUN file descriptor, intercepts TCP flows,
 * tunnels each flow through a local SOCKS5 proxy, and writes response packets
 * back into the TUN device.
 *
 * Only IPv4/TCP is handled. UDP and ICMP are dropped silently (you can extend
 * this class to handle UDP via SOCKS5 UDP ASSOCIATE if required).
 *
 * Threading model:
 *   - One coroutine reads packets from TUN (tunReadLoop).
 *   - Per-connection coroutines handle I/O to SOCKS5 and write back to TUN.
 *   - All TUN writes are serialised through [tunOut].
 */
class TunWorker(
    private val tunFd: FileDescriptor,
    private val socksPort: Int,
    private val scope: CoroutineScope
) {
    companion object {
        private const val TAG = "TunWorker"
        private const val MTU = 1500
        private const val SOCKS5_HOST = "127.0.0.1"

        // SOCKS5 constants
        private const val SOCKS_VERSION: Byte = 0x05
        private const val SOCKS_AUTH_NONE: Byte = 0x00
        private const val SOCKS_CMD_CONNECT: Byte = 0x01
        private const val SOCKS_ATYP_IPV4: Byte = 0x01
        private const val SOCKS_ATYP_DOMAIN: Byte = 0x03

        // TCP flags
        private const val TCP_SYN: Int = 0x02
        private const val TCP_ACK: Int = 0x10
        private const val TCP_FIN: Int = 0x01
        private const val TCP_RST: Int = 0x04
        private const val TCP_PSH: Int = 0x08
    }

    // Key: (srcIp, srcPort, dstIp, dstPort)
    private data class FlowKey(val srcIp: Int, val srcPort: Int, val dstIp: Int, val dstPort: Int)

    private data class FlowState(
        val socket: Socket,
        val sockOut: OutputStream,
        val job: Job,
        @Volatile var clientSeq: Long,      // Next seq we expect from client
        @Volatile var serverSeq: Long,      // Next seq we send to client
        @Volatile var established: Boolean
    )

    private val flows = ConcurrentHashMap<FlowKey, FlowState>()
    private val tunIn = FileInputStream(tunFd)
    private val tunOut = FileOutputStream(tunFd)
    @Volatile private var running = false
    private var readJob: Job? = null

    fun start() {
        running = true
        readJob = scope.launch(Dispatchers.IO) { tunReadLoop() }
        Log.i(TAG, "TunWorker started, SOCKS5=:$socksPort")
    }

    fun stop() {
        running = false
        readJob?.cancel()
        flows.values.forEach { it.job.cancel(); runCatching { it.socket.close() } }
        flows.clear()
        runCatching { tunIn.close() }
        runCatching { tunOut.close() }
        Log.i(TAG, "TunWorker stopped")
    }

    // -------------------------------------------------------------------------
    // TUN read loop
    // -------------------------------------------------------------------------

    private fun tunReadLoop() {
        val buf = ByteArray(MTU)
        while (running) {
            val len = try {
                tunIn.read(buf)
            } catch (e: Exception) {
                if (running) Log.w(TAG, "TUN read error", e)
                break
            }
            if (len <= 0) continue

            val pkt = ByteBuffer.wrap(buf, 0, len).order(ByteOrder.BIG_ENDIAN)
            handlePacket(pkt, len)
        }
    }

    // -------------------------------------------------------------------------
    // Packet dispatch
    // -------------------------------------------------------------------------

    private fun handlePacket(pkt: ByteBuffer, len: Int) {
        if (len < 20) return  // Too short for IPv4 header

        val versionIhl = pkt.get(0).toInt() and 0xFF
        val version = versionIhl shr 4
        if (version != 4) return  // IPv6 not handled

        val ihl = (versionIhl and 0x0F) * 4
        if (len < ihl) return

        val protocol = pkt.get(9).toInt() and 0xFF
        if (protocol != 6) return  // Only TCP

        handleTcpPacket(pkt, ihl, len)
    }

    // -------------------------------------------------------------------------
    // TCP packet handling
    // -------------------------------------------------------------------------

    private fun handleTcpPacket(pkt: ByteBuffer, ihl: Int, len: Int) {
        if (len < ihl + 20) return

        val srcIp = pkt.getInt(12)
        val dstIp = pkt.getInt(16)
        val srcPort = pkt.getShort(ihl).toInt() and 0xFFFF
        val dstPort = pkt.getShort(ihl + 2).toInt() and 0xFFFF
        val seqNum = pkt.getInt(ihl + 4).toLong() and 0xFFFFFFFFL
        val ackNum = pkt.getInt(ihl + 8).toLong() and 0xFFFFFFFFL
        val dataOffset = ((pkt.get(ihl + 12).toInt() and 0xFF) shr 4) * 4
        val flags = pkt.get(ihl + 13).toInt() and 0xFF
        val tcpPayloadStart = ihl + dataOffset
        val tcpPayloadLen = len - tcpPayloadStart

        val key = FlowKey(srcIp, srcPort, dstIp, dstPort)

        when {
            flags and TCP_SYN != 0 && flags and TCP_ACK == 0 -> {
                // New connection
                handleSyn(key, seqNum, dstIp, dstPort)
            }

            flags and TCP_RST != 0 -> {
                closeFlow(key)
            }

            flags and TCP_FIN != 0 -> {
                // Send FIN-ACK and close
                val flow = flows[key] ?: return
                sendTcpPacket(
                    srcIp = dstIp, srcPort = dstPort,
                    dstIp = srcIp, dstPort = srcPort,
                    seq = flow.serverSeq,
                    ack = (seqNum + 1L) and 0xFFFFFFFFL,
                    flags = TCP_FIN or TCP_ACK,
                    payload = ByteArray(0)
                )
                closeFlow(key)
            }

            flags and TCP_ACK != 0 && tcpPayloadLen > 0 -> {
                // Data segment
                val flow = flows[key] ?: return
                val payload = ByteArray(tcpPayloadLen)
                pkt.position(tcpPayloadStart)
                pkt.get(payload)
                scope.launch(Dispatchers.IO) {
                    try {
                        flow.sockOut.write(payload)
                        flow.sockOut.flush()
                        flow.clientSeq = (seqNum + tcpPayloadLen.toLong()) and 0xFFFFFFFFL
                        // ACK the data
                        sendTcpPacket(
                            srcIp = dstIp, srcPort = dstPort,
                            dstIp = srcIp, dstPort = srcPort,
                            seq = flow.serverSeq,
                            ack = flow.clientSeq,
                            flags = TCP_ACK,
                            payload = ByteArray(0)
                        )
                    } catch (e: Exception) {
                        Log.w(TAG, "Forward to SOCKS5 failed", e)
                        closeFlow(key)
                    }
                }
            }
        }
    }

    // -------------------------------------------------------------------------
    // SYN handler — connects to SOCKS5 and replies with SYN-ACK
    // -------------------------------------------------------------------------

    private fun handleSyn(key: FlowKey, clientSeq: Long, dstIp: Int, dstPort: Int) {
        if (flows.containsKey(key)) return  // Already have a flow

        val dstIpBytes = ByteArray(4) { i -> (dstIp shr (24 - i * 8)).toByte() }
        val dstIpStr = InetAddress.getByAddress(dstIpBytes).hostAddress ?: return

        scope.launch(Dispatchers.IO) {
            try {
                val sock = Socket()
                sock.connect(InetSocketAddress(SOCKS5_HOST, socksPort), 5000)
                sock.tcpNoDelay = true

                // SOCKS5 handshake
                socks5Connect(sock, dstIpStr, dstPort)

                val serverInitSeq = System.currentTimeMillis() and 0xFFFFFFFFL
                val nextClientSeq = (clientSeq + 1L) and 0xFFFFFFFFL

                val flow = FlowState(
                    socket = sock,
                    sockOut = sock.getOutputStream(),
                    job = Job(),
                    clientSeq = nextClientSeq,
                    serverSeq = (serverInitSeq + 1L) and 0xFFFFFFFFL,
                    established = true
                )
                flows[key] = flow

                // SYN-ACK
                sendTcpPacket(
                    srcIp = dstIp, srcPort = dstPort,
                    dstIp = key.srcIp, dstPort = key.srcPort,
                    seq = serverInitSeq,
                    ack = nextClientSeq,
                    flags = TCP_SYN or TCP_ACK,
                    payload = ByteArray(0)
                )

                // Start relay: SOCKS5 → TUN
                flow.job.also { parentJob ->
                    scope.launch(Dispatchers.IO + parentJob) {
                        socksToTunRelay(key, flow, sock)
                    }
                }

            } catch (e: Exception) {
                Log.w(TAG, "SOCKS5 connect failed for $dstIpStr:$dstPort", e)
                // Send RST back to client
                sendTcpPacket(
                    srcIp = dstIp, srcPort = dstPort,
                    dstIp = key.srcIp, dstPort = key.srcPort,
                    seq = 0L, ack = (clientSeq + 1L) and 0xFFFFFFFFL,
                    flags = TCP_RST or TCP_ACK,
                    payload = ByteArray(0)
                )
            }
        }
    }

    // -------------------------------------------------------------------------
    // Relay from SOCKS5 socket → TUN device
    // -------------------------------------------------------------------------

    private suspend fun socksToTunRelay(key: FlowKey, flow: FlowState, sock: Socket) {
        val sockIn = sock.getInputStream()
        val buf = ByteArray(MTU - 40)  // Leave room for IP+TCP headers
        try {
            while (running && !sock.isClosed) {
                val n = sockIn.read(buf)
                if (n < 0) break
                if (n == 0) continue

                val payload = buf.copyOf(n)
                sendTcpPacket(
                    srcIp = key.dstIp, srcPort = key.dstPort,
                    dstIp = key.srcIp, dstPort = key.srcPort,
                    seq = flow.serverSeq,
                    ack = flow.clientSeq,
                    flags = TCP_PSH or TCP_ACK,
                    payload = payload
                )
                flow.serverSeq = (flow.serverSeq + n.toLong()) and 0xFFFFFFFFL
            }
        } catch (e: Exception) {
            if (running) Log.w(TAG, "SOCKS5→TUN relay error", e)
        } finally {
            // Send FIN to client
            sendTcpPacket(
                srcIp = key.dstIp, srcPort = key.dstPort,
                dstIp = key.srcIp, dstPort = key.srcPort,
                seq = flow.serverSeq,
                ack = flow.clientSeq,
                flags = TCP_FIN or TCP_ACK,
                payload = ByteArray(0)
            )
            closeFlow(key)
        }
    }

    // -------------------------------------------------------------------------
    // SOCKS5 handshake
    // -------------------------------------------------------------------------

    private fun socks5Connect(sock: Socket, host: String, port: Int) {
        val out = sock.getOutputStream()
        val inp = sock.getInputStream()

        // Greeting: VER=5, NMETHODS=1, METHOD=NO_AUTH
        out.write(byteArrayOf(SOCKS_VERSION, 1, SOCKS_AUTH_NONE))
        out.flush()

        // Server choice
        val choice = inp.read()
        if (choice != 5) throw IllegalStateException("SOCKS5: bad version $choice")
        val method = inp.read()
        if (method != 0) throw IllegalStateException("SOCKS5: auth required (method=$method)")

        // CONNECT request
        val hostBytes = host.toByteArray(Charsets.US_ASCII)
        val req = ByteBuffer.allocate(7 + hostBytes.size)
        req.put(SOCKS_VERSION)
        req.put(SOCKS_CMD_CONNECT)
        req.put(0x00)                       // reserved
        req.put(SOCKS_ATYP_DOMAIN)
        req.put(hostBytes.size.toByte())
        req.put(hostBytes)
        req.putShort(port.toShort())
        out.write(req.array())
        out.flush()

        // Server reply (min 10 bytes)
        val reply = ByteArray(10)
        var read = 0
        while (read < 10) {
            val n = inp.read(reply, read, 10 - read)
            if (n < 0) throw IllegalStateException("SOCKS5: connection closed during reply")
            read += n
        }
        if (reply[1] != 0x00.toByte()) {
            throw IllegalStateException("SOCKS5: server replied with error code ${reply[1].toInt() and 0xFF}")
        }
        // Consume any extra bytes in reply (for domain/IPv6 bound addresses)
        when (reply[3].toInt() and 0xFF) {
            0x01 -> { /* IPv4 — already read 10 bytes, done */ }
            0x03 -> {
                val domLen = inp.read()
                val extra = ByteArray(domLen + 2)
                inp.read(extra)
            }
            0x04 -> {
                // IPv6 (16 bytes) + 2 port bytes — already have 4 of the 16 address bytes
                val extra = ByteArray(12 + 2)
                inp.read(extra)
            }
        }
    }

    // -------------------------------------------------------------------------
    // Flow cleanup
    // -------------------------------------------------------------------------

    private fun closeFlow(key: FlowKey) {
        val flow = flows.remove(key) ?: return
        flow.job.cancel()
        runCatching { flow.socket.close() }
    }

    // -------------------------------------------------------------------------
    // Build and write an IPv4/TCP packet to the TUN device
    // -------------------------------------------------------------------------

    private fun sendTcpPacket(
        srcIp: Int, srcPort: Int,
        dstIp: Int, dstPort: Int,
        seq: Long, ack: Long,
        flags: Int,
        payload: ByteArray
    ) {
        val tcpHeaderLen = 20
        val ipHeaderLen = 20
        val totalLen = ipHeaderLen + tcpHeaderLen + payload.size

        val buf = ByteBuffer.allocate(totalLen).order(ByteOrder.BIG_ENDIAN)

        // ---- IPv4 header ----
        buf.put(0x45.toByte())                             // version=4, IHL=5
        buf.put(0x00)                                       // DSCP/ECN
        buf.putShort(totalLen.toShort())
        buf.putShort(0)                                     // identification
        buf.putShort(0x4000.toShort())                      // flags: DF
        buf.put(64)                                         // TTL
        buf.put(6)                                          // protocol: TCP
        buf.putShort(0)                                     // checksum placeholder
        buf.putInt(srcIp)
        buf.putInt(dstIp)

        val ipCsum = ipChecksum(buf.array(), 0, ipHeaderLen)
        buf.putShort(10, ipCsum.toShort())

        // ---- TCP header ----
        val tcpStart = ipHeaderLen
        buf.putShort((srcPort and 0xFFFF).toShort())
        buf.putShort((dstPort and 0xFFFF).toShort())
        buf.putInt((seq and 0xFFFFFFFFL).toInt())
        buf.putInt((ack and 0xFFFFFFFFL).toInt())
        buf.put(((tcpHeaderLen / 4) shl 4).toByte())        // data offset
        buf.put((flags and 0xFF).toByte())
        buf.putShort(65535.toShort())                       // window size
        buf.putShort(0)                                     // checksum placeholder
        buf.putShort(0)                                     // urgent pointer

        // payload
        if (payload.isNotEmpty()) buf.put(payload)

        // TCP checksum (over pseudo-header + TCP)
        val tcpCsum = tcpChecksum(buf.array(), srcIp, dstIp, tcpHeaderLen + payload.size)
        buf.putShort(tcpStart + 16, tcpCsum.toShort())

        try {
            synchronized(tunOut) {
                tunOut.write(buf.array())
            }
        } catch (e: Exception) {
            if (running) Log.w(TAG, "TUN write error", e)
        }
    }

    // -------------------------------------------------------------------------
    // Checksum utilities
    // -------------------------------------------------------------------------

    /** One's complement sum over [len] bytes of [data] starting at [offset]. */
    private fun onesComplementSum(data: ByteArray, offset: Int, len: Int): Int {
        var sum = 0
        var i = offset
        while (i < offset + len - 1) {
            val high = data[i].toInt() and 0xFF
            val low = data[i + 1].toInt() and 0xFF
            sum += (high shl 8) or low
            i += 2
        }
        if ((len and 1) != 0) {
            sum += (data[offset + len - 1].toInt() and 0xFF) shl 8
        }
        while (sum shr 16 != 0) {
            sum = (sum and 0xFFFF) + (sum shr 16)
        }
        return sum
    }

    private fun ipChecksum(data: ByteArray, offset: Int, len: Int): Int {
        return onesComplementSum(data, offset, len).inv() and 0xFFFF
    }

    private fun tcpChecksum(pkt: ByteArray, srcIp: Int, dstIp: Int, tcpLen: Int): Int {
        // Pseudo-header: src, dst, zero, proto=6, tcp length
        val pseudo = ByteBuffer.allocate(12).order(ByteOrder.BIG_ENDIAN)
        pseudo.putInt(srcIp)
        pseudo.putInt(dstIp)
        pseudo.put(0)
        pseudo.put(6)
        pseudo.putShort(tcpLen.toShort())

        var sum = onesComplementSum(pseudo.array(), 0, 12)
        sum += onesComplementSum(pkt, 20, tcpLen)   // TCP segment starts at byte 20
        while (sum shr 16 != 0) {
            sum = (sum and 0xFFFF) + (sum shr 16)
        }
        return sum.inv() and 0xFFFF
    }
}
