# Бенчмарк vk-vpn (режим whitelist / relay)

## Когда считать «relay-сессию»

В `app.log` (joiner):

- `ICE uses TURN relay` или `ICE bypass via TURN relay`
- Номинированная пара: `relay/...` на **local** стороне

На сервере часто: `srflx <-> prflx` при том же пути — это нормально (другая сторона stats).

## Чеклист одной сессии

1. Connect → `VPN Connected` **< 10 с** после `VP8 TUNNEL CONNECTED` (не 15+ с ожидания).
2. Нет `watchdog unhealthy — ICE restart` во время curl.
3. `curl.exe -o NUL -w "bytes=%{size_download} time=%{time_total}s\n" "http://mirror.yandex.ru/.../ubuntu-24.04.4-netboot-amd64.tar.gz"`
4. 2ip.ru → страна VPS.
5. В `serverlog`: `relay: close id=N ... rx=...` для curl — `rx` близко к размеру файла.

## Env

| Переменная | По умолчанию (тест) | Назначение |
|------------|---------------------|------------|
| `VK_VPN_ICE_TRANSPORT_POLICY` | **relay** (в коде) | TURN-only на время тестов; релиз: default `all` (pion auto) |
| `VK_VPN_ICE_TRANSPORT_POLICY=all` | — | Временно вернуть pion auto (host/srflx/prflx) |
| `VK_VPN_ICE_RELAY_WAIT` | 8s | Только при `policy=all`: pion ждёт direct |
| `VK_VPN_ICE_PREFER_DIRECT_WAIT` | 3s | Ожидание direct перед redirect (только при direct ICE) |
| `VK_VPN_ICE_PREFER_DIRECT_WAIT=0` | — | Сразу redirect |
| `VK_VPN_EXTRA_BYPASS_IPS` | — | **Обязательно для SSH к VPS** при relay-only, напр. `185.103.101.245` |

### SSH / journalctl при включённом VPN

При `ICETransportPolicy=relay` в bypass попадают только TURN IP (90.156.x), **не** IP вашего VPS.  
Трафик `ssh user@185.103.x` уходит **внутрь VPN** → broken pipe, зависание терминала.

**Решение:** перед Connect задать `VK_VPN_EXTRA_BYPASS_IPS=<IP VPS>` или отключать VPN для SSH.

## Ожидаемая скорость (relay + VP8)

| Уровень | Mbps |
|---------|------|
| Плохо | < 1 |
| Норма | 2–8 |
| Хорошо | 8–15 |
| Потолок VP8 pacing | ~6.5 Mbps target в логе |

Сравнение с Marzban на том же VPS: Marzban часто 50–500+ Mbps; vk-vpn — другой транспорт.
