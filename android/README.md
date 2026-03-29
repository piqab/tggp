# Hysteria2 Proxy — Android

Android VPN-клієнт для підключення до [mtproxy](../README.md) сервера через Hysteria2 (QUIC/UDP).
Весь трафік пристрою маршрутизується через зашифрований тунель.

## Як це працює

```
Device apps
    ↓ (all TCP/IP traffic)
Android VpnService  ──  TUN interface (10.0.0.1/24)
    ↓
TunWorker  ──  parses IPv4/TCP packets  →  local SOCKS5 (127.0.0.1:10808)
    ↓
Go hysteria2 client  ──  QUIC stream per connection
    ↓
Hysteria2 server  ──  YOUR_SERVER:HYSTERIA2_PORT (UDP)
    ↓
Internet
```

1. VpnService створює TUN-інтерфейс і перехоплює весь IP-трафік
2. TunWorker читає TCP-пакети, встановлює SOCKS5-з'єднання для кожного потоку
3. Go-бібліотека (gomobile) реалізує Hysteria2-протокол поверх QUIC
4. Кожне TCP-з'єднання — окремий QUIC-стрім до сервера

## Структура

```
android/
├── go/
│   ├── hysteria2.go        — Go пакет: QUIC-клієнт + локальний SOCKS5
│   ├── go.mod              — залежність quic-go v0.59.0
│   └── build.sh            — gomobile bind → hysteria2.aar
└── app/
    ├── build.gradle.kts
    ├── libs/               — сюди кладеться hysteria2.aar після збірки
    └── src/main/
        ├── AndroidManifest.xml
        └── kotlin/com/proxy/hysteria/
            ├── App.kt              — Application, notification channel
            ├── Config.kt           — SharedPreferences (server/port/password)
            ├── MainActivity.kt     — UI: поля, кнопка, статус
            ├── ProxyVpnService.kt  — VpnService: TUN + Go клієнт
            └── TunWorker.kt        — tun2socks: IPv4/TCP → SOCKS5
```

## Вимоги для збірки

- **Go 1.23+** (для quic-go v0.59)
- **Android NDK** r25+
- **gomobile** (`go install golang.org/x/mobile/cmd/gomobile@latest`)
- **Android Studio** Hedgehog (2023.1.1) або новіший
- **JDK 17**

## Збірка

### Крок 1 — Зібрати Go AAR

```bash
cd android/go

# Встановити gomobile (один раз)
go install golang.org/x/mobile/cmd/gomobile@latest
gomobile init

# Зібрати бібліотеку для Android
./build.sh
# або вручну:
# gomobile bind -target=android -o ../app/libs/hysteria2.aar .
```

Результат: `android/app/libs/hysteria2.aar`

> **Примітка**: `build.sh` вимагає встановленого Android NDK.
> Якщо NDK не знайдено, встанови його через Android Studio:
> **SDK Manager → SDK Tools → NDK (Side by side)**
> або вкажи шлях: `export ANDROID_NDK_HOME=~/Android/Sdk/ndk/<version>`

### Крок 2 — Відкрити в Android Studio

1. Відкрий папку `android/` в Android Studio
2. **File → Sync Project with Gradle Files**
3. **Build → Build Bundle(s) / APK(s) → Build APK(s)**

або через командний рядок:

```bash
cd android
./gradlew assembleDebug
# APK: app/build/outputs/apk/debug/app-debug.apk
```

## Налаштування сервера

Перед використанням застосунку запусти [mtproxy](../README.md) з увімкненим Hysteria2:

```bash
SECRETS="alice:dd..."        \
PORT=443                     \
HYSTERIA2_PORT=8443          \
HYSTERIA2_PASSWORD=mypassword \
./mtproxy
```

## Використання

1. Встанови APK на пристрій
2. Введи дані підключення:
   - **Server** — IP або домен сервера
   - **Port** — UDP порт Hysteria2 (за замовч. `8443`)
   - **Password** — `HYSTERIA2_PASSWORD` з конфігурації сервера
   - **Skip TLS verify** — увімкни якщо сервер використовує self-signed сертифікат
3. Натисни **Connect**
4. Підтвердь запит на VPN-з'єднання
5. Статус зміниться на **Connected** — весь трафік йде через тунель

## Налагодження

### Логи Go-клієнта

```bash
adb logcat -s hysteria2
```

### Логи VPN-сервісу

```bash
adb logcat -s ProxyVpnService TunWorker
```

### Поширені помилки

| Помилка | Причина | Рішення |
|---------|---------|---------|
| `QUIC dial: timeout` | Сервер недосяжний | Перевір IP/порт, фаєрвол (UDP!) |
| `auth: server rejected` | Неправильний пароль | Перевір `HYSTERIA2_PASSWORD` |
| `QUIC dial: tls: failed` | Невалідний сертифікат | Увімкни **Skip TLS verify** |
| VPN не підключається | Не прийнятий запит | Перевір налаштування VPN Android |
| `listen :10808: address in use` | Порт зайнятий | Перезапусти застосунок |

## Протокол (коротко)

Hysteria2 over QUIC — кастомний протокол поверх TLS 1.3 + QUIC (UDP).

**Auth stream** (перший стрім, без type-байту):
```
Client → Server:  [uint16 len][password bytes][uint64 rx=0]
Server → Client:  [uint8 ok=1][uint64 rx][uint16 msg_len][msg]
```

**TCP proxy stream** (кожне з'єднання):
```
Client → Server:  [uint8 type=0x01][uint16 addr_len][host:port][uint32 req_id=0]
Server → Client:  [uint8 ok=1][uint16 msg_len=0]
→ bidirectional relay
```

## Безпека

- TLS 1.3 мінімум
- ALPN `h3` — трафік виглядає як HTTP/3
- Рекомендується використовувати реальний TLS-сертифікат (Let's Encrypt):
  встанови `HYSTERIA2_CERT` і `HYSTERIA2_KEY` на сервері, вимкни **Skip TLS verify** в застосунку

## Ліцензія

MIT
