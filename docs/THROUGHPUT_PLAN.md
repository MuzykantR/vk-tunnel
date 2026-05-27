# План: максимальный throughput vk-vpn в `relay` режиме

**Status:** В работе. **Цель НЕ достигнута.** Текущая фаза: T-A (RTCP defaults restoration).
**Predecessor:** `docs/TRANSPORT_FIX_PLAN.md` — план «100% доставка» (закрыт ✅).
**Current state:** см. `docs/TRANSPORT_AUDIT.md`.

> **История одной строкой:** baseline 1.3 Mbps → подозрение на GCC → пробовали отключить (T-GCC v1) + явно сконфигурировать (T-GCC v2) → оба не сработали → перешли к гипотезе DPI-сигнатуры на ChannelData payload → реализовали protocol-aware WRAP (T-WRAP) → **WRAP не помог**. После замера с мобильной сети cap локализован на **VK-стороне** (один и тот же ~1.3 Mbps с RefillRate ~140 KB/s и BurstSize ~600 KB на разных ISP). Текущая ставка — **отсутствие RTCP feedback** от receiver'а заставляет SFU держать default mobile-grade budget. Тест A это проверяет.

---

## 0. TL;DR (актуально на 2026-05-27)

- **Baseline на default config:** ~1.3 Mbps sustained (одинаково в `tunnelMode=kcp` и `tunnelMode=video` в relay).
- **Форма трафика:** token bucket с RefillRate ~140 KB/s + BurstSize ~600 KB. Бурсты до 280-350 KB/s (~2.8 Mbps) в течение 3-4 секунд, потом 5-6 секунд hold на baseline. Одинаково на домашнем WiFi и мобильном LTE → cap **не на пути от клиента к VK**.
- **Что НЕ сработало:** KCP env-sweep (T1), bonding (T3 — RR+blocking dead-end), GCC tuning v1/v2 (T-GCC), protocol-aware WRAP с ChaCha20 на ChannelData payload (T-WRAP).
- **Текущая рабочая гипотеза:** SFU не получает RTCP feedback (TWCC/NACK/RR) от нашего receiver'а потому что pion стартует с **пустым InterceptorRegistry**, держит per-track budget в самом низком профиле. Test A это проверяет — `RegisterDefaultInterceptors` на обеих сторонах.
- **Альтернативная гипотеза (если T-A не сработает):** SFU делает bitstream-level parsing VP8 payload, видит мусор после первого байта (наш hardcoded fake header + ChaCha20-encrypted KCP внутри), clamp'ит в fallback bucket. Test B + C это проверяют.
- **Цель:** sustained ≥ 5 Mbps минимум, ≥ 10 Mbps стрейч. **Не достигнута.**

---

## 1. Закрытые гипотезы (отрицательно)

| ID | Гипотеза | Статус | Причина закрытия |
|---|---|---|---|
| H1 | KCP env-sweep (WINDOW/FEC/NODELAY) | ❌ | T1: defaults оптимальны, все варианты хуже или шум |
| H4 | Bonding 2+ KCP sub-tunnel'ов в одной сессии | ❌ | T3: RR+blocking write дедлок, плюс per-session cap всё равно скорее всего есть |
| H11 | Per-track cap (probe keepalive 200/500 pps) | ❌ | T2: cover-traffic не влияет на curl throughput, cap не per-track |
| H12 | vkturnproxy-стиль обфускации (без WebRTC) | ❌ | Архитектурно несовместимо с pion'ом в relay-mode |
| H13 v1 | Пустой InterceptorRegistry (отключить всё) | ❌ | T-GCC v1: 52 Mbps был routing leak (VPN не поднялся), реальный sustained = 0.3-0.5 Mbps |
| H13 v2 | GCC с hardcoded floor + LeakyBucketPacer | ❌ | T-GCC v2: UDP flood до DTLS handshake → SFU дропает сессию |
| H14 | QUIC-over-VP8 | ❌ | Anton48 (vkturnproxy) уже пробовал — то же что KCP |
| H15 | DPI шейпит ChannelData payload по сигнатуре | ❌ | T-WRAP: protocol-aware ChaCha20 на bytes 4+ не сдвинул cap. Либо VK уже обновил DPI, либо cap не от payload-signature |

---

## 2. Текущая фаза: T-A — RTCP defaults restoration

**Гипотеза:** pion без `RegisterDefaultInterceptors` — это «silent receiver». SFU не получает TWCC timing feedback, NACK loss reports, RR/SR. Без этих сигналов SFU **по умолчанию консервативен** и держит per-track budget на mobile-grade ~1.3 Mbps. Это **точно** соответствует форме нашего token bucket.

**Реализация (ветка `feat/rtcp-defaults`):**
- Новый файл `vk-vpn-client/webrtc/rtcp_defaults.go` — helper `InstallRTCPDefaults(me, who)` вызывает `webrtc.RegisterDefaultInterceptors(me, ir)` и возвращает Registry. Gated через `VK_VPN_RTCP_DEFAULTS` env, default ON.
- `joiner.go` + `session.go` — передают полученный Registry в `webrtc.NewAPI(..., WithInterceptorRegistry(ir))`.
- Makefile: `make rtcp N=0|1` / `make rtcp-clear` / `make rtcp-show` по аналогии с `make wrap`.

**Что регистрируется:**
- `nack.ResponderInterceptor` — receiver-side, отвечает NACK'ом на loss
- `nack.GeneratorInterceptor` — sender-side, ретрансмит по NACK
- `twcc.HeaderExtensionInterceptor` — sender-side, штампует RTP с transport-wide seq
- `twcc.SenderInterceptor` — receiver-side, шлёт TWCC feedback с arrival times
- `report.SenderInterceptor` / `report.ReceiverInterceptor` — стандартные RTCP SR/RR
- **НЕ включён GCC pacer** — нам не нужно своё CC, нужно чтобы SFU своё повысил

**Acceptance criteria T-A:**
- Минимум: sustained > 2 Mbps × 3 прогона curl 92 MB
- Хорошо: sustained ≥ 5 Mbps
- Идеально: ≥ 10 Mbps (значит гипотеза попала точно)

**Если T-A не сработает** — переходим к T-B (pseudo-valid VP8 prefix) перед коммитом в дорогой T-C.

---

## 3. Будущие фазы (запланированные, не начатые)

### T-B — Pseudo-valid VP8 prefix
**Идея:** заменить наш hardcoded `vp8Interframe` (17 байт фейкового header'а) на более структурно-плотный prefix с реальным VP8 uncompressed header'ом (frame_type=inter, show_frame=1, version=0). Partition 0 остаётся мусором. Цена: ~4-6 часов.

**Решает:** проверка частичной чувствительности SFU к bitstream-валидности.

### T-C — Full synthetic VP8 frame (Frankenstein Frame)
**Идея:** реально валидный Partition 0 (макроблоки + motion vectors для статической картинки 1280×720), наш encrypted KCP инжектится **только в Partitions 1..N (DCT coefficients)**, где высокая энтропия ожидаема. Цена: 2-4 недели.

**Решает:** полное прохождение SFU bitstream validation, если она существует. Делается **только** если T-A и T-B не дали результата.

---

## 4. История замеров

| Дата | Конфиг | curl avg | Тип | Комментарий |
|---|---|---|---|---|
| 2026-05-24 | bonding=1, GCC=on, defaults | 1.31 Mbps | sustained 10 мин | Hybrid-B baseline |
| 2026-05-25 | bonding=1, GCC=on, KCP-sweep × 4 | 0.51-1.35 Mbps | sustained | T1: env-tuning исчерпан |
| 2026-05-25 | bonding=2 (RR dispatch) | 0 Mbps (stall) | failed | T3: blocking write deadlock |
| 2026-05-26 | bonding=1, GCC=off (empty registry) | 0.5-1.0 Mbps | sustained | T-GCC v1: cap не снят |
| 2026-05-26 | bonding=1, GCC v2 (hardcoded floor) | connection failed | failed | T-GCC v2: UDP flood до handshake |
| 2026-05-27 | bonding=1, WRAP ON (ChaCha20 ChannelData) | ~1.3 Mbps | sustained | T-WRAP: cap не снят |
| 2026-05-27 | WRAP ON, мобильная LTE сеть | 140 KB/s avg, 350 KB/s bursts | sustained | Cap локализован на VK-стороне |
| **2026-05-27** | **WRAP ON, RTCP defaults ON** | **TBD** | **pending** | **Test A — ожидает замера** |

---

## 5. Что НЕ гарантировано (для T-A и далее)

1. **SFU реально использует RTCP feedback для upgrade'а budget'а.** Возможно VK SFU имеет жёсткий cap независимо от feedback'а — тогда T-A не поможет.
2. **TWCC sender-side BWE не дерётся с SFU shaping.** Pion's TWCC может само начать throttle'ить когда видит «overcrowded» сигнал от собственного receiver'а (loopback effect через TURN).
3. **NACK retransmits не подорвут KCP ARQ.** Pion может ретрансмитить пакеты которые KCP уже считает «потерянными и заретрансмитченными» — потенциально дублирование. На текущей пропускной способности не критично.

---

## 6. Архитектурные решения по итогам предыдущих фаз

| Артефакт | Решение | Причина |
|---|---|---|
| `TURNWrapEnabled()` default | **ON** | Cheap defense-in-depth, не задерживает handshake, инфраструктура остаётся для возможного v2 inline-counter |
| Pion's GCC interceptor | **Не использовать** (явно отключено) | Дрался с KCP CC, ломал bandwidth |
| KCP `NoCongestion=1`, `Window=2048`, `FEC=10:3` | **Оптимум по T1** | Все альтернативы хуже |
| `tunnelMode=kcp` (default через systemd) | **Сохранён** | Reliable delivery поверх unreliable VP8, ARQ + FEC компенсируют 3-6% physical loss |

---

## 7. Резюме одной строкой

Cap ~1.3 Mbps **точно на VK-стороне** (одинаково на двух разных ISP). Форма — **token bucket с RefillRate ~140 KB/s** что говорит про **default mobile-grade per-track budget**. Текущая ставка: receiver не шлёт RTCP feedback → SFU держит низкий budget. Test A это проверяет за один прогон curl.
