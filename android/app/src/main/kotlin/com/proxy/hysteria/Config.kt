package com.proxy.hysteria

import android.content.Context
import android.content.SharedPreferences

/**
 * Holds VPN / proxy configuration and persists it to SharedPreferences.
 */
data class Config(
    val server: String,
    val port: Int,
    val password: String,
    val insecure: Boolean,
    val localSocksPort: Int = DEFAULT_SOCKS_PORT
) {
    companion object {
        const val DEFAULT_SOCKS_PORT = 10808
        private const val PREFS_NAME = "hysteria2_config"
        private const val KEY_SERVER = "server"
        private const val KEY_PORT = "port"
        private const val KEY_PASSWORD = "password"
        private const val KEY_INSECURE = "insecure"
        private const val KEY_SOCKS_PORT = "socks_port"

        fun load(context: Context): Config {
            val prefs: SharedPreferences =
                context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
            return Config(
                server = prefs.getString(KEY_SERVER, "") ?: "",
                port = prefs.getInt(KEY_PORT, 8443),
                password = prefs.getString(KEY_PASSWORD, "") ?: "",
                insecure = prefs.getBoolean(KEY_INSECURE, false),
                localSocksPort = prefs.getInt(KEY_SOCKS_PORT, DEFAULT_SOCKS_PORT)
            )
        }
    }

    fun save(context: Context) {
        val prefs: SharedPreferences =
            context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
        prefs.edit()
            .putString(KEY_SERVER, server)
            .putInt(KEY_PORT, port)
            .putString(KEY_PASSWORD, password)
            .putBoolean(KEY_INSECURE, insecure)
            .putInt(KEY_SOCKS_PORT, localSocksPort)
            .apply()
    }

    fun isValid(): Boolean = server.isNotBlank() && port in 1..65535 && password.isNotBlank()
}
