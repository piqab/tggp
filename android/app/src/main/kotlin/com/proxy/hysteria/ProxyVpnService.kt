package com.proxy.hysteria

import android.app.Notification
import android.app.PendingIntent
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.util.Log
import androidx.core.app.NotificationCompat
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch

/**
 * Android VPN service that:
 *  1. Starts the Go Hysteria2 SOCKS5 client via the gomobile binding.
 *  2. Creates a TUN interface capturing all IPv4 traffic.
 *  3. Passes the TUN fd to [TunWorker] which translates TCP flows through SOCKS5.
 */
class ProxyVpnService : VpnService() {

    companion object {
        private const val TAG = "ProxyVpnService"

        // Intent actions
        const val ACTION_CONNECT = "com.proxy.hysteria.CONNECT"
        const val ACTION_DISCONNECT = "com.proxy.hysteria.DISCONNECT"
        const val ACTION_QUERY_STATUS = "com.proxy.hysteria.QUERY_STATUS"

        // Broadcast action for UI updates
        const val ACTION_STATUS = "com.proxy.hysteria.STATUS"
        const val EXTRA_STATUS = "status"
        const val EXTRA_CONNECTED = "connected"

        // Intent extras
        const val EXTRA_SERVER = "server"
        const val EXTRA_PORT = "port"
        const val EXTRA_PASSWORD = "password"
        const val EXTRA_INSECURE = "insecure"
        const val EXTRA_SOCKS_PORT = "socks_port"

        // Volatile so MainActivity can read it safely from any thread
        @Volatile var isRunning: Boolean = false
            private set
    }

    private val serviceScope = CoroutineScope(Dispatchers.IO + SupervisorJob())

    private var tunPfd: ParcelFileDescriptor? = null
    private var tunWorker: TunWorker? = null

    // -------------------------------------------------------------------------
    // Lifecycle
    // -------------------------------------------------------------------------

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        return when (intent?.action) {
            ACTION_CONNECT -> {
                val server = intent.getStringExtra(EXTRA_SERVER) ?: ""
                val port = intent.getIntExtra(EXTRA_PORT, 8443)
                val password = intent.getStringExtra(EXTRA_PASSWORD) ?: ""
                val insecure = intent.getBooleanExtra(EXTRA_INSECURE, false)
                val socksPort = intent.getIntExtra(EXTRA_SOCKS_PORT, Config.DEFAULT_SOCKS_PORT)

                startForegroundNotification()
                connect(server, port, password, insecure, socksPort)
                START_STICKY
            }

            ACTION_DISCONNECT -> {
                disconnect()
                START_NOT_STICKY
            }

            ACTION_QUERY_STATUS -> {
                broadcastStatus(isRunning, if (isRunning) getString(R.string.status_connected) else getString(R.string.status_disconnected))
                START_NOT_STICKY
            }

            else -> {
                // Service restarted by system after being killed
                START_STICKY
            }
        }
    }

    override fun onDestroy() {
        super.onDestroy()
        cleanup()
        serviceScope.cancel()
    }

    // -------------------------------------------------------------------------
    // Connect / disconnect
    // -------------------------------------------------------------------------

    private fun connect(
        server: String,
        port: Int,
        password: String,
        insecure: Boolean,
        socksPort: Int
    ) {
        serviceScope.launch {
            broadcastStatus(false, getString(R.string.status_connecting))

            try {
                // 1. Start Go Hysteria2 SOCKS5 client
                Log.d(TAG, "Starting Go Hysteria2 client → $server:$port  socks=:$socksPort")
                hysteria2.Hysteria2.start(server, port.toLong(), password, insecure, socksPort.toLong())

                // 2. Build TUN interface
                val builder = Builder()
                    .setSession("Hysteria2VPN")
                    .addAddress("10.0.0.1", 24)
                    .addDnsServer("8.8.8.8")
                    .addDnsServer("8.8.4.4")
                    .addRoute("0.0.0.0", 0)          // Route all IPv4
                    .setMtu(1500)
                    .setBlocking(false)               // Non-blocking reads

                // Exclude our own app so it can reach the real server
                builder.addDisallowedApplication(packageName)

                tunPfd = builder.establish()
                    ?: throw IllegalStateException("VpnService.Builder.establish() returned null")

                // 3. Start TUN worker
                val worker = TunWorker(tunPfd!!.fileDescriptor, socksPort, serviceScope)
                tunWorker = worker
                worker.start()

                isRunning = true
                broadcastStatus(true, getString(R.string.status_connected))
                Log.i(TAG, "VPN connected")

            } catch (e: Exception) {
                Log.e(TAG, "Connect failed", e)
                broadcastStatus(false, getString(R.string.status_error, e.message ?: "unknown"))
                cleanup()
                stopSelf()
            }
        }
    }

    private fun disconnect() {
        serviceScope.launch {
            broadcastStatus(false, getString(R.string.status_disconnecting))
            cleanup()
            broadcastStatus(false, getString(R.string.status_disconnected))
            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
        }
    }

    private fun cleanup() {
        isRunning = false

        tunWorker?.stop()
        tunWorker = null

        try {
            tunPfd?.close()
        } catch (e: Exception) {
            Log.w(TAG, "Error closing TUN fd", e)
        }
        tunPfd = null

        try {
            hysteria2.Hysteria2.stop()
        } catch (e: Exception) {
            Log.w(TAG, "Error stopping Go client", e)
        }
    }

    // -------------------------------------------------------------------------
    // Foreground notification
    // -------------------------------------------------------------------------

    private fun startForegroundNotification() {
        val pendingFlags =
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M)
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
            else
                PendingIntent.FLAG_UPDATE_CURRENT

        val openAppIntent = PendingIntent.getActivity(
            this, 0,
            Intent(this, MainActivity::class.java),
            pendingFlags
        )

        val disconnectIntent = PendingIntent.getService(
            this, 1,
            Intent(this, ProxyVpnService::class.java).apply { action = ACTION_DISCONNECT },
            pendingFlags
        )

        val notification: Notification = NotificationCompat.Builder(this, App.CHANNEL_ID_VPN)
            .setSmallIcon(android.R.drawable.ic_dialog_info)
            .setContentTitle(getString(R.string.notif_title))
            .setContentText(getString(R.string.notif_text))
            .setContentIntent(openAppIntent)
            .addAction(
                android.R.drawable.ic_delete,
                getString(R.string.btn_disconnect),
                disconnectIntent
            )
            .setOngoing(true)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .build()

        startForeground(App.NOTIFICATION_ID_VPN, notification)
    }

    // -------------------------------------------------------------------------
    // Broadcast helpers
    // -------------------------------------------------------------------------

    private fun broadcastStatus(connected: Boolean, status: String) {
        val intent = Intent(ACTION_STATUS).apply {
            putExtra(EXTRA_CONNECTED, connected)
            putExtra(EXTRA_STATUS, status)
            setPackage(packageName)
        }
        sendBroadcast(intent)
    }
}
