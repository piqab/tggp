# Add project specific ProGuard rules here.

# Keep gomobile-generated classes
-keep class hysteria2.** { *; }
-keep class go.** { *; }

# Keep VpnService subclass
-keep class com.proxy.hysteria.ProxyVpnService { *; }
