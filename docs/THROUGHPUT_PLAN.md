# План: максимальный throughput vk-vpn в `relay` режиме

**Status:** новый план, **требует утверждения** перед стартом реализации.
**Predecessor:** `docs/TRANSPORT_FIX_PLAN.md` — план «100% доставка» (закрыт ✅).
**Current state:** см. `docs/TRANSPORT_AUDIT.md`.
**Branch base:** `feat/transport-rework`.

> Цель плана 100% доставки достигнута — теперь нужна **скорость**. Sustained 1.2 Mbps не годится для нормального серфинга и тем более для видео. Этот план — про **выжимание максимума** в архитектуре Hybrid-B (KCP-over-VP8 + relay).

---

## 0. TL;DR

- **Текущее:** sustained ~1.2 Mbps, burst до 2.3 Mbps, 100% доставка.
- **Цель ближняя:** sustained ≥ 5 Mbps (комфортный web + СМИ/чатинг/документы).
- **Цель стрейч:** sustained ≥ 10 Mbps (видеозвонки, потоковое HD).
- **Физический cap:** один TURN-канал VK сегодня выдаёт ~2-3 Mbps на наш профиль трафика. Чтобы пробить cap, нужно либо **уменьшить overhead** в нашем стеке, либо **пользоваться несколькими каналами** (bonding).
- Старый `docs/THROUGHPUT_HYPOTHESES.md` остаётся ценным источником гипотез, но в нём есть **устаревшие пункты** (см. §1).

---

## 1. Ревизия `THROUGHPUT_HYPOTHESES.md` — что осталось актуальным

Проверка каждой гипотезы из старого документа против текущего состояния:

### A. Путь ICE — **актуально**

| # | Гипотеза | Статус сегодня |
|---|----------|----------------|
| A1 | Всегда **relay**, не host/srflx | Так и есть (продуктовое требование). Не лечим. |
| A2 | Uplink дома лимитирует | Возможно. Не проверен в KCP-mode. **Перепроверить** в чистом эксперименте. |
| A3 | Bufferbloat на Wi-Fi | Возможно. Не проверен. |
| A4 | MTU 1400 vs 1500 | KCP MTU 1200 уже учитывает overhead. Поднимать опасно (риск IP-фрагментации в TURN UDP). |
| A5 | VPS egress/NIC cap | Не проверен. `iperf3` с VPS без VPN — обязательное условие. |

### B. Один канал VP8 — **частично устарело**

| # | Гипотеза | Статус сегодня |
|---|----------|----------------|
| B1 | Pacing не выжимает pipe (`writerLoop` / sendQueue) | ❌ **Устарело в KCP mode**. KCP не использует наш writerLoop — он сам управляет pacing. |
| B2 | Пик 50 Mbps = burst → token bucket VK | ✅ **Подтверждено**. Видим в KCP-rate: пульсация 4 → 600 kbps → 8 → 1600 kbps. Token bucket работает. |
| B3 | Много TCP через один VP8 → внутренняя очередь | ⚠️ **Усугубилось в KCP**. Все TCP-flows конкурируют за окно KCP. **Лечится** через `VK_VPN_RELAY_CONNECT_LIMIT` либо архитектурно через multi-stream KCP. |
| B4 | Крупные кадры редко в логе | Диагностически закрыто — `vp8-reasm` метрика работает. |
| B5 | Обратный канал тоже VP8 | В KCP так и есть. Если UL не нужен — теоретически выделение отдельного TX-канала ничем не поможет, наш use case — DL-heavy. |

### C. Параметры VP8 — **устарело**

`VK_VPN_VP8_FPS` / `VK_VPN_VP8_BATCH` влияют только на legacy `video` mode. В KCP mode pacing определяется параметрами KCP, не VP8.

### D. «Ещё техники» — переоценка

| # | Идея | Статус сегодня |
|---|------|----------------|
| D1 | Второй video track (screen share) | ✅ **Самая перспективная** для bonding'а. Hybrid-C из старого плана. |
| D2 | Opus/audio как data tunnel | ❌ bitrate cap делает бессмысленным для bulk. |
| D3 | Dual VP8 (2 video tracks) | ✅ То же, что D1, под другим углом. Совместимо. |
| D4 | DC + VP8 split | ❌ DC throughput 53 kbps — split не имеет смысла. |
| D5 | Unordered DC для части трафика | ⚠️ Спорно. UDP/DNS через unreliable — okay, но DNS в нашем туннеле уже через `MsgUDP` + одиночные пакеты. |
| D6 | H264/AV1 вместо VP8 | ⚠️ Гипотеза. Если VK даёт больше bitrate на H264 (как «лучше сжатый» codec) — стоит попробовать. KCP внутри codec'а — ему всё равно. |
| D7 | Меньше obfuscation | ⚠️ ChaCha20 на современном CPU — copy speed. Не bottleneck. Не лезем. |
| D8 | Raw RTP без fake VP8 | ❌ Антифрод-риск. |

### E. Несколько звонков (bonding по сессиям) — **не делать**

Старый план это исключал, причина та же: высокий VK-risk.

### F. Стабильность вуз/дом — **не throughput**

Отложено. Этим план не занимается.

---

## 2. Свои гипотезы — рычаги для нового плана

В порядке «дёшево → дорого» и «низкий риск → высокий»:

### H1. **KCP-tuning sweep** — дёшево, низкий риск

Текущие defaults `1,10,2,1` + `MTU 1200` + `FEC 10:3` + `window 2048` — выбраны интуитивно. Возможно, не оптимальны для TURN-канала с 60-90 ms RTT и 2-12% loss.

Эксперименты:

| Параметр | Default | A/B значения | Ожидание |
|----------|---------|--------------|----------|
| `VK_VPN_KCP_NODELAY` | `1,10,2,1` | `1,5,2,1` (тик 5 ms) | +pps → +throughput на чистых интервалах |
| `VK_VPN_KCP_FEC` | `10:3` | `10:1` (меньше parity) | +18% полезной полосы, риск retransmit на потерях |
| `VK_VPN_KCP_FEC` | `10:3` | `off` | A/B на гипотезу «FEC только мешает в KCP» |
| `VK_VPN_KCP_WINDOW` | `2048` | `4096` / `8192` | Поможет если ограничение по window |
| `VK_VPN_KCP_MTU` | `1200` | `1100` (под IP-fragment cap) | Минус fragmentation — плюс reliable rate |

**Acceptance:** sustained throughput замерен через 5-минутный curl, разница ≥ 20% от baseline.

**Риск:** низкий — все параметры через env, откат без пересборки.

### H2. **Лимит параллельных CONNECT** — дёшево, низкий риск

В одном KCP-stream несколько TCP-flows конкурируют. Браузер открывает 30-50+ параллельных HTTP. Каждый получает 1/30 от полосы. На 1.2 Mbps это 40 kbps на flow — недостаточно для TLS-handshake до timeout.

`VK_VPN_RELAY_CONNECT_LIMIT=N` уже существует. Подобрать `N` (8-16) и замерить subjective UX.

**Acceptance:** среднее время загрузки страницы (Lighthouse / DevTools) уменьшается. Throughput не меняется (важно), но **полезная скорость** растёт.

### H3. **TURN-server retry** — средне, средний риск

VK даёт 2 TURN-сервера в signaling. Pion выбирает один по ICE. Если выбранный медленный — мы залипаем на нём всю сессию.

Идея: в начале сессии **попробовать оба** (mini-benchmark — отправить 100 KB и измерить latency / throughput), выбрать лучший. Через `OfferOptions{IceRestart}` или через явный pion ICE.

**Acceptance:** при двух TURN'ах с разной пропускной способностью выбирается лучший в 80%+ прогонов.

**Риск:** усложняет signaling-логику. Возможен flap'инг.

### H4. **Bonding: dual-KCP над двумя VP8 tracks** — дорого, средний риск

Hybrid-C из старого плана. Добавить второй `addtrack` VP8 в SDP, запустить второй `KCPTunnel` на нём, на joiner-сидя merge'ить через app-level sequence number.

Архитектурно:

```
RelayBridge.SendData(frame) → load_balance(kcp1, kcp2) → frame идёт по одному из двух
                                                      ← merge_by_seqno на приёме
```

Технические сложности:

- Frame-level sequence number (раньше не нужен был, протокол беззапятый).
- Reorder buffer на receiver (если frame#5 пришёл через kcp1 раньше frame#4 через kcp2).
- VK TURN policer'ит **каждый track отдельно** или **сессию в целом**? Неизвестно. Если первое — bonding даст ×2. Если второе — ×1.

**Acceptance:** sustained throughput ≥ 80% от 2× baseline.

**Риск:** средний. Кодовый объём ~1000 строк. **Может ничего не дать** если VK капит сессию суммарно.

### H5. **VPS ближе** — операционно, не код

Текущий VPS, видимо, не в той же AS, что VK TURN. RTT 60-90 ms на TURN-пути.

Если поставить VPS в ту же страну/город, что TURN сервер (Россия, Москва), RTT может упасть до 5-20 ms. **BDP** уменьшится → KCP recover'ится после потерь быстрее → sustained ↑.

**Acceptance:** замер RTT до TURN с разных VPS, выбор VPS с минимальным RTT.

**Риск:** мы перейдём на новый VPS — это операционная задача, не код.

### H6. **iperf3 / pure UDP baseline через TURN** — диагностика

Перед всеми оптимизациями измерить «потолок UDP» через TURN:

- Заскриптовать pion-app, который тупо шлёт UDP в TURN без KCP/SCTP/VP8.
- Замерить sustained pps и Mbps.
- Это **теоретический потолок** нашей трубы.

Если UDP-потолок = 5 Mbps, а мы выжимаем 1.2 Mbps → есть запас, оптимизируем стек.
Если UDP-потолок = 1.5 Mbps → мы у потолка, никакой оптимизацией не вылезем, нужен H4 или H5.

**Это критично сделать первым.** Иначе мы будем оптимизировать вслепую.

---

## 3. Поэтапный план — порядок экспериментов

### Фаза T1 — Базовый замер (1-2 вечера)

| # | Действие | Файл результата |
|---|----------|------------------|
| T1.1 | curl 92 MB × 5 итераций (текущий код) — baseline | `bench-results/throughput_baseline_<date>.txt` |
| T1.2 | Сделать **pure UDP baseline** (см. H6): тестовая утилита, замеряющая pps к TURN'у | `scripts/turn-udp-iperf.go` (новый) |
| T1.3 | Замерить RTT до текущего TURN'а с разных географических точек VPS (если есть доступ) | `bench-results/turn-rtt.txt` |

**Решение по этим замерам:**

- Если UDP baseline >> KCP throughput — продолжать оптимизацию (Фазы T2-T3).
- Если UDP baseline ≈ KCP throughput — пропустить T2-T3, идти на T4 (bonding) или T5 (VPS).

### Фаза T2 — KCP tuning sweep (H1)

| # | A/B | Замер |
|---|-----|-------|
| T2.1 | `NODELAY=1,5,2,1` | curl 92 MB |
| T2.2 | `FEC=10:1` | curl 92 MB |
| T2.3 | `FEC=off` | curl 92 MB |
| T2.4 | `WINDOW=4096` | curl 92 MB |
| T2.5 | `MTU=1100` | curl 92 MB |
| T2.6 | Лучшая комбинация из T2.1-T2.5 | curl 92 MB × 3 |

Все правки — через env (без пересборки). После выбора best — **зашить в defaults в `kcp_env.go`**.

**Acceptance Фазы T2:** sustained ≥ 1.5× baseline.

### Фаза T3 — UX-улучшения (H2)

| # | Действие | Замер |
|---|----------|-------|
| T3.1 | `VK_VPN_RELAY_CONNECT_LIMIT=8` | Среднее время загрузки 10 сайтов |
| T3.2 | Адаптивный CONNECT limit (на сервере: если sendQueue near full → стоп новые CONNECT) | Тот же набор |

**Acceptance:** **subjective UX** браузера — страницы открываются быстрее, чат не виснет.

### Фаза T4 — Bonding (H4)

Делается **только** если Фазы T2-T3 не дотянули до 5 Mbps.

| # | Действие | Файл |
|---|----------|------|
| T4.1 | Добавить frame-level sequence number в protocol (новый MsgType или поле в существующих) | `tunnel/protocol.go` |
| T4.2 | Reorder buffer в RelayBridge на receiver | `tunnel/relay_bridge.go` |
| T4.3 | Второй `TrackLocalStaticSample` в `joiner.go`/`session.go` | webrtc init |
| T4.4 | Второй `KCPTunnel` поверх второго track'а | session/joiner |
| T4.5 | Round-robin / weighted дисперсия frames по двум tunnel'ям | `tunnel/bonding.go` (новый) |
| T4.6 | Sanity-замеры: bonding включён vs выключен | scripts |

**Acceptance:** sustained ≥ 1.8× baseline. Если ≤ 1.2× — VK капит сессию, bonding не помог, **откатываем**.

### Фаза T5 — VPS ближе (H5)

Если bonding не дал × 2 — пробуем новый VPS близко к TURN-серверам.

| # | Действие | Метрика |
|---|----------|---------|
| T5.1 | Деплой на VPS в России (или ближе к TURN) | RTT до TURN |
| T5.2 | curl 92 MB через новый deploy | sustained Mbps |

**Acceptance:** sustained ≥ 5 Mbps.

### Фаза T6 — Финальный отчёт

После любой фазы, в которой acceptance достигнут:

- Создать `docs/THROUGHPUT_REPORT.md` с фактическими цифрами.
- Зашить лучшие defaults в код.
- Обновить AUDIT.

---

## 4. Acceptance criteria для плана в целом

- **Минимум:** sustained 5 Mbps в curl 92 MB × 3 итерации.
- **Стрейч:** sustained 10 Mbps, видеозвонок 720p проходит.
- **Не ломать:** 100% delivery должна сохраняться (regression test против `TRANSPORT_FIX_PLAN.md`).

---

## 5. Что я НЕ гарантирую

1. **Что мы выжмем 10 Mbps в relay**. Если VK TURN'и капят сессию суммарно — bonding не поможет, и физический cap останется.
2. **Что любая оптимизация даст линейный прирост**. KCP-CC и FEC взаимосвязаны: меньше FEC → больше retransmit на потерях → throughput может **упасть**.
3. **Что субъективный UX = throughput**. На малой полосе важнее **меньше параллельных соединений**, чем «выжать больше».

---

## 6. Что прошу утвердить **до** реализации

1. **Согласны ли с порядком «сначала pure UDP baseline (H6), потом всё остальное»?** Или сразу начнём с KCP-tuning?
2. **Готовы ли** к Фазе T4 (bonding) если T2-T3 не дотянут? Это ~1000 LoC и риск регрессии.
3. **Готовы ли** к Фазе T5 (новый VPS)? Это операционная задача — нужен ли вообще?
4. **Хочешь ли** держать legacy `video` mode (без ARQ) для исследований, или удалить ради простоты?

---

## 7. Чек-лист быстрых проверок (без кода) — можно сделать прямо сейчас

| # | Эксперимент | Что замерить |
|---|-------------|--------------|
| Q1 | `$env:VK_VPN_KCP_FEC = "off"; curl 92 MB` | sustained vs default 10:3 |
| Q2 | `$env:VK_VPN_KCP_FEC = "10:1"; curl 92 MB` | sustained vs default |
| Q3 | `$env:VK_VPN_KCP_NODELAY = "1,5,2,1"; curl 92 MB` | sustained |
| Q4 | `$env:VK_VPN_KCP_WINDOW = "4096"; curl 92 MB` | sustained |
| Q5 | Speedtest через VPN с текущими defaults — записать ID | DL / UL / ping |
| Q6 | Speedtest без VPN — записать (baseline линии) | DL / UL / ping |

Эти 6 замеров за час дадут нам **карту чувствительности** к KCP-параметрам. С ними мы сможем умно подобрать sweep'ы Фазы T2.

---

## 8. Структура будущих коммитов

```
HEAD (после fix(relay) statefuls framing)
├─ throughput-opt
│
├─ bench(turn): pure UDP baseline tooling
├─ feat(kcp): tune defaults — interval=5, fec=10:1, window=4096
├─ feat(relay): adaptive CONNECT limit
├─ feat(transport): dual-VP8 bonding with frame-level seqno  ← если нужно
├─ docs: THROUGHPUT_REPORT.md
└─ chore: update AUDIT.md with throughput numbers
```

---

## 9. Резюме

Цель плана `TRANSPORT_FIX_PLAN.md` (100% доставка) **достигнута**. Текущая sustained-скорость 1.2 Mbps — это про **reliability over speed**: KCP+FEC доставляют всё, но платят 30% overhead и упираются в TURN-cap. Чтобы вылезти за 5 Mbps, нужно:

1. Сначала измерить **физический потолок** TURN-канала (pure UDP baseline) — это критично, иначе оптимизируем вслепую.
2. Подкрутить KCP-параметры (это **дёшево, низкий риск**) и понять, сколько ещё в стеке overhead'а.
3. Только если этого не хватит — идти на bonding (двойной KCP), что **гораздо дороже** и может ничего не дать, если VK капит сессию суммарно.

Минимум для нормальной жизни сервиса — sustained ≥ 5 Mbps. Это позволит обычный веб-серфинг и низкокачественное видео. Стрейч — 10+ Mbps.
