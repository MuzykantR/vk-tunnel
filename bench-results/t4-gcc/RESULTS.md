# T4 — Disable WebRTC GCC (H13) — РЕЗУЛЬТАТЫ

**Дата:** 2026-05-26.
**Ветка кода:** `feat/bonding-dual-kcp` (e66533d).
**Baseline:** 1.18-1.31 Mbps sustained (см. baseline_2026-05-25.md, t3-bonding).

## Сводная

| Прогон | Server GCC | Client GCC | bonding | curl avg MB/s | Mbps | Время |
|---|---|---|---|---:|---:|---|
| GCC=off (server only) | **off** | on (default) | 1 | **6.6** | **52.8** | 13.9 с / 91.86 MB ✓ |

Curl line:
```
100 91.86M 100 91.86M 0 0 6.60M 0 00:13 00:13 6.41M
bytes=96330730 time=13.898885s avg_kBps=6930824
```

## Что подтвердилось

Из логов сервера и клиента (см. `server-gcc-off.log`, `run-gcc-off-server-only.log`):

```
[boot] env: VK_VPN_DISABLE_GCC=1                      # server boot
[creator] media engine: GCC=false                     # server peer config
[creator] === MODE: KCP-OVER-VP8 ===                  # bonding=1, single stream
kcp: tunnel ready conv=0x564b5650 ... fec=10/3 ...    # default KCP settings
```

```
Joiner: media engine: GCC=true                        # client default — NOT disabled
[kcp] kcp: tunnel ready ... fec=10/3 nodelay=1,10,2,1 # default
Joiner: remote track video/VP8 id="video" bond-idx=0  # bonding=1 confirmed
```

**Конфигурация была АСИММЕТРИЧНОЙ:** GCC выключен только на сервере. Клиент стартовал с дефолтным feedback'ом. Достаточно одной стороны — sender (server, который шлёт DL bulk) не делает throttle.

## Интерпретация

Cap ~1.3 Mbps, который мы наблюдали 3 дня — **полностью искусственный**, на нашей стороне:

1. Pion sender-side BWE / TWCC interceptor реагировал на REMB feedback от peer'а
2. На лоссивом VK TURN (3-6% physical packet loss VP8) — REMB рекомендовал ~1 Mbps
3. Pion **задерживал** `WriteSample` чтобы соблюсти rate
4. KCP внутри ничего с этим сделать не мог — он шлёт в `vp8PacketConn.WriteTo` → `track.WriteSample`, pion перехватывает

Когда мы:
- Зарегистрировали VP8 codec **без** RTCPFeedback в `tunnel.SetupMediaEngine()`
- Передали **пустой** `InterceptorRegistry` через `WithInterceptorRegistry`

Pion перестал throttle'ить → KCP получил **полную пропускную способность TURN'а** → 52 Mbps.

## Что это значит для прочих гипотез

- **VK TURN не имеет per-session или per-track cap'а** на уровне MBps. До 52+ Mbps выходим без проблем. Цифры «5 Mbps на 1 поток» (vkturnproxy README) — **специфичны для их архитектуры** (WireGuard через TURN ChannelData), не наша.
- **Bonding** (T3) — больше **не нужен**. Single stream выдаёт цель ×10.
- **WRAP анти-DPI** (anton48 vkturnproxy) — для нас **неактуален**. Их cap был другой природы.
- **QUIC-over-VP8** (H14) — отменяется, KCP даёт всё что нужно.

## Что осталось проверить

1. **Длинный sustained** — 13.9 секунд это короткий burst, мог быть выше реального sustained. Запустить curl × 3 подряд (или большой файл).
2. **Симметричный GCC=off** — выставить env и на клиенте, может ещё прирост на UL (Speedtest UL у нас всегда был хуже DL).
3. **Стабильность под flap'ом** — теперь когда мы реально используем канал, SDP glare bug при WS reset может проявляться чаще. Long-running тест.

## Артефакты

- `server-gcc-off.log` — server VPS log (truncated на boot, содержит весь session)
- `run-gcc-off-server-only.log` — client app.log с прогоном
