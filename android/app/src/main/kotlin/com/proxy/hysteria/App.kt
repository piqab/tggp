package com.proxy.hysteria

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.os.Build

class App : Application() {

    companion object {
        const val CHANNEL_ID_VPN = "vpn_service"
        const val NOTIFICATION_ID_VPN = 1001
    }

    override fun onCreate() {
        super.onCreate()
        createNotificationChannels()
    }

    private fun createNotificationChannels() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val vpnChannel = NotificationChannel(
                CHANNEL_ID_VPN,
                getString(R.string.notif_channel_vpn_name),
                NotificationManager.IMPORTANCE_LOW
            ).apply {
                description = getString(R.string.notif_channel_vpn_desc)
                setShowBadge(false)
            }

            val manager = getSystemService(NotificationManager::class.java)
            manager.createNotificationChannel(vpnChannel)
        }
    }
}
