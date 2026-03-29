package com.proxy.hysteria

import android.app.Activity
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.view.View
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import com.google.android.material.color.MaterialColors
import com.proxy.hysteria.databinding.ActivityMainBinding

class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding

    // Receives status updates from ProxyVpnService
    private val statusReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            when (intent.action) {
                ProxyVpnService.ACTION_STATUS -> {
                    val status = intent.getStringExtra(ProxyVpnService.EXTRA_STATUS) ?: return
                    val connected = intent.getBooleanExtra(ProxyVpnService.EXTRA_CONNECTED, false)
                    updateUiState(connected, status)
                }
            }
        }
    }

    // Launcher for VPN permission dialog
    private val vpnPermissionLauncher = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { result ->
        if (result.resultCode == Activity.RESULT_OK) {
            startVpnService()
        } else {
            Toast.makeText(this, R.string.vpn_permission_denied, Toast.LENGTH_SHORT).show()
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)

        loadConfig()
        setupClickListeners()
    }

    override fun onResume() {
        super.onResume()
        val filter = IntentFilter(ProxyVpnService.ACTION_STATUS)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            registerReceiver(statusReceiver, filter, RECEIVER_NOT_EXPORTED)
        } else {
            @Suppress("UnspecifiedRegisterReceiverFlag")
            registerReceiver(statusReceiver, filter)
        }

        // Ask service for current state
        val req = Intent(this, ProxyVpnService::class.java).apply {
            action = ProxyVpnService.ACTION_QUERY_STATUS
        }
        startService(req)
    }

    override fun onPause() {
        super.onPause()
        try {
            unregisterReceiver(statusReceiver)
        } catch (_: IllegalArgumentException) { /* was not registered */ }
    }

    // -------------------------------------------------------------------------
    // UI helpers
    // -------------------------------------------------------------------------

    private fun loadConfig() {
        val cfg = Config.load(this)
        binding.editServer.setText(cfg.server)
        binding.editPort.setText(if (cfg.port > 0) cfg.port.toString() else "8443")
        binding.editPassword.setText(cfg.password)
        binding.checkInsecure.isChecked = cfg.insecure
    }

    private fun setupClickListeners() {
        binding.btnConnect.setOnClickListener {
            if (ProxyVpnService.isRunning) {
                stopVpn()
            } else {
                connectVpn()
            }
        }
    }

    private fun connectVpn() {
        val server = binding.editServer.text.toString().trim()
        val portStr = binding.editPort.text.toString().trim()
        val password = binding.editPassword.text.toString().trim()
        val insecure = binding.checkInsecure.isChecked

        // Validate
        if (server.isEmpty()) {
            binding.editServer.error = getString(R.string.error_server_required)
            return
        }
        val port = portStr.toIntOrNull()
        if (port == null || port !in 1..65535) {
            binding.editPort.error = getString(R.string.error_port_invalid)
            return
        }
        if (password.isEmpty()) {
            binding.editPassword.error = getString(R.string.error_password_required)
            return
        }

        val cfg = Config(server, port, password, insecure)
        cfg.save(this)

        // Request VPN permission if needed
        val vpnIntent = VpnService.prepare(this)
        if (vpnIntent != null) {
            vpnPermissionLauncher.launch(vpnIntent)
        } else {
            startVpnService()
        }
    }

    private fun startVpnService() {
        val cfg = Config.load(this)
        val intent = Intent(this, ProxyVpnService::class.java).apply {
            action = ProxyVpnService.ACTION_CONNECT
            putExtra(ProxyVpnService.EXTRA_SERVER, cfg.server)
            putExtra(ProxyVpnService.EXTRA_PORT, cfg.port)
            putExtra(ProxyVpnService.EXTRA_PASSWORD, cfg.password)
            putExtra(ProxyVpnService.EXTRA_INSECURE, cfg.insecure)
            putExtra(ProxyVpnService.EXTRA_SOCKS_PORT, cfg.localSocksPort)
        }
        ContextCompat.startForegroundService(this, intent)
        updateUiState(connecting = true, status = getString(R.string.status_connecting))
    }

    private fun stopVpn() {
        val intent = Intent(this, ProxyVpnService::class.java).apply {
            action = ProxyVpnService.ACTION_DISCONNECT
        }
        startService(intent)
        updateUiState(connecting = false, status = getString(R.string.status_disconnecting))
    }

    /**
     * @param connecting true = fully connected; false = disconnected; use separate param for
     *                   intermediate "connecting…" state.
     */
    private fun updateUiState(connecting: Boolean, status: String) {
        binding.textStatus.text = status

        if (connecting) {
            binding.btnConnect.text = getString(R.string.btn_disconnect)
            binding.statusIndicator.setBackgroundResource(R.drawable.indicator_connected)
            setInputsEnabled(false)
        } else {
            binding.btnConnect.text = getString(R.string.btn_connect)
            binding.statusIndicator.setBackgroundResource(R.drawable.indicator_disconnected)
            setInputsEnabled(true)
        }
    }

    private fun setInputsEnabled(enabled: Boolean) {
        binding.editServer.isEnabled = enabled
        binding.editPort.isEnabled = enabled
        binding.editPassword.isEnabled = enabled
        binding.checkInsecure.isEnabled = enabled
    }
}
