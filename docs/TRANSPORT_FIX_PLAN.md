# План: 100 % доставка vk-vpn в `relay` режиме

**Branch:** `feat/transport-rework` (откачено к `fb37254`).
**Status:** **План утверждён пользователем 2026-05-23.** Идём по порядку: Фаза 1 (cleanup) → Фаза 2 (замеры в relay) → выбор архитектуры между Hybrid-A/B/C по результатам.
**Companion:** `docs/TRANSPORT_AUDIT.md` — аудит того, что сейчас в коде.

> Этот документ написан с позиции Principal Engineer / архитектора, не «yes-man'а». Часть аргументов идёт **против** предложения из `ARCHITECTURE_AUDIT_ARQ.md`. Это сознательно — план прошёл через ревью пользователя.

## ⚠ Поправка от 2026-05-23 (после диалога с пользователем)

Первоначальный план предлагал «DC default в relay». **Это отменено** — пользователь поднял аргумент об **anti-fraud / DPI** на VK TURN: профиль «video=keepalive, DC=18 Mbps» — явная аномалия по сравнению с реальным звонком. VP8 как cover-канал — **обязательная часть** маскировки.

Поэтому варианты, рассматриваемые в Фазе 3:

- **Hybrid-A** = VP8 cover noise (500 kbps–1.5 Mbps) + DC bulk (надёжный SCTP). 100% доставка бесплатно от SCTP. Подходит **если DC throughput в relay ≥ ~10 Mbps**.
- **Hybrid-B** = VP8 bulk (как сейчас) + DC только под ACK/NACK/RTO (TCP-Lite). Маскировка идеальная. Реализация дорогая. Если DC throughput недостаточен — это путь.
- **Hybrid-C** = bonding (оба канала несут bulk). DPI-профиль хуже, чем у A/B. Откладываем.

Конкретная архитектура (A vs B) выбирается **после** замеров Фазы 2.

---

## 0. Что нужно решить **до** реализации

| # | Решение | Опции | Моя рекомендация |
|---|---------|-------|------------------|
| D1 | Какой bulk-транспорт делаем основным в relay | (a) SCTP DC, (b) VP8+ARQ, (c) KCP/QUIC over VP8 | **(a) — но только после измерения** |
| D2 | Когда чистим bugs lifecycle (close races, half-close, inCh drop) | (i) до выбора транспорта, (ii) после | **(i) до — иначе бенчмарк будет ложным** |
| D3 | Что делаем с VP8 mode | (α) оставляем как fallback, (β) удаляем | **(α) — мост на случай SFU/throttle** |
| D4 | Ставим ли quic/kcp Go-library | да / нет | **нет, пока DC не измерили** |

Эти 4 точки — то, что я прошу утвердить. Дальше уже декомпозиция.

---

## 1. Критика предложения `ARCHITECTURE_AUDIT_ARQ.md`

Проголосовал «за» предыдущий ИИ. Я согласен **частично**.

### Что в предложении правильно

- Диагноз «curl 84 % = потеря RTP внутри VP8» — **верный**. См. `TRANSPORT_AUDIT.md` §4.2.
- Утверждение «нельзя жить без app-level reliability поверх VP8» — **верное**, если VP8 остаётся bulk-каналом.
- Sliding Window + cumulative ACK + RTO + NACK на reliable control plane — это **классический TCP-Lite дизайн**. Алгоритмически без дыр.
- Cache evict только по ACK — корректно.
- Backpressure через блокировку чтения local TCP — корректно.

### Что в предложении я считаю **спорным или прямо неверным**

#### А. Реализуется TCP в user space там, где SCTP уже даёт TCP-семантику

WebRTC DataChannel **уже** работает поверх SCTP. SCTP:

- надёжен, упорядочен, фрагментирует/собирает;
- имеет congestion control (SCTP-CC, см. RFC 4960);
- имеет flow control (RWND);
- реализован в pion и продакшен-проверен.

Предложение `ARCHITECTURE_AUDIT_ARQ.md` фактически **переизобретает SCTP в user space** только потому, что bulk идёт через VP8 RTP track вместо DataChannel. Это абсурдно сложный путь, **если для перехода на SCTP DC нет физических препятствий**.

#### Б. Аргумент «VP8 быстрее SFU не режет» **не применим к relay-only**

В docs неоднократно: «VK SFU policer'ом режет non-video bitrate». **В TURN-relay path SFU нет.** TURN — это L3-форвардер UDP-пакетов; ему всё равно, что в payload — SRTP или SCTP/DTLS. Throughput-разница между DC и VP8 на relay будет определяться:

- overhead протокола (SCTP > SRTP на несколько процентов),
- congestion control (SCTP-CC может быть консервативнее, чем pion-VP8 pacing'а который CC игнорит).

Это **измеримо**, и доказательно того, что DC будет хуже — на текущий момент нет ни в одном бенчмарке проекта.

#### В. Предыдущая команда уже 4 раза билась об эту стену

`AGENTS_LOG.md` фиксирует Phase 3.1, 3.2, 3.3, 3.4, 4 — каждая итерация ARQ ломала throughput, ломала curl, вводила deadlocks. Phase 4 (sliding window) до сих пор «Pending User Verification». Это не значит, что сделать TCP-Lite невозможно — но это означает, что **risk profile реализации высокий**, а полевая отработка нулевая. Реинвестировать в путь с такой историей — не лучшая идея, если есть **более простой вариант с DC**.

#### Г. ACK plane / bulk plane контеншн

Авторы предложения сами признают: «ACKs flow through SCTP DataChannel. If DC becomes congested, our control loop lags». В relay-only это **в одной UDP-трубе TURN'а**. ACK пакеты могут уйти в очередь за VP8-burst'ом, RTT по ACK раздуется, sliding window замрёт, throughput упадёт. Митигация (batch ACK раз в 50 ms) — это уже компромисс между latency и throughput, который нужно тюнить, и от которого мы ждём «магического числа».

#### Д. Sequence wrap, replay defense, deadlock prevention

Все эти пункты в предложении упомянуты («32-bit wrap», «discard duplicate SeqNum», «select with stopCh»). Каждый из них — отдельный класс багов, на котором уже падали (см. Phase 3.2 «strict ARQ delivery» — head-of-line block).

### Итоговый вердикт по предложению

> «ARQ over VP8 — это правильный путь, если мы вынуждены оставаться на VP8. В relay-only мы **не вынуждены** оставаться на VP8. Поэтому путь A (DC) должен быть проверен **до** того, как мы вкладываемся в путь B (ARQ).»

---

## 2. Свои идеи

### Идея 1 (главная). **DC mode как default для `relay`**.

- Код уже это поддерживает (`VK_VPN_TUNNEL_MODE=dc`).
- Гарантия 100 % доставки — встроена в SCTP.
- Throughput неизвестен в проде relay-only. Бенчмарк перед коммитом.

### Идея 2. **TCP half-close + drain на server side** (универсально).

Сейчас server рвёт сокет по EOF от origin. Из-за этого «хвостовой stall». Фикс одинаково помогает обоим режимам и нужен **независимо** от выбора bulk-канала.

### Идея 3. **Чистый lifecycle `socksConn`**.

Убираем `sync.Once` для `startAppWriter`, добавляем atomic `closed`, делаем `deliverToApp` атомарно проверяющим `closed` под мьютексом и/или через `select` с `done` каналом. Убирает классы паник, описанных в `TRANSPORT_AUDIT.md` §7.1.

### Идея 4. **Backpressure вместо drop в `dcConn.inCh`**.

Сейчас upstream данные (`MsgData` от joiner на creator) тихо дропаются при переполнении. Должны блокировать SCTP-read loop (через bounded chan и блокирующий send). Это потребует увеличения `inCh` или прямой записи в `conn.Write` под мьютексом — взвесим в фазе.

### Идея 5 (запасная). **Если DC throughput недостаточен → KCP-Go, не свой ARQ.**

KCP-go — battle-tested library (~3k LoC, продакшен-проверена в shadowsocks-kcptun). Даёт ARQ + опциональный FEC. Заворачиваем bulk в KCP-stream, отправляем как payload через тот же VP8 sample. Это **намного менее рискованно**, чем писать sliding window руками.

### Идея 6 (радикальная, для рассмотрения **не сейчас**). **Custom TCP-over-TURN**.

«Кастомный TCP» в формулировке пользователя я понимаю двояко:

- (a) **TCP-семантика поверх UDP** — это что даёт KCP/QUIC. Реально полезно если DC не справится.
- (b) **Реальная TCP-аллокация через TURN-TCP (RFC 6062)** — pion-ice её не реализует из коробки. Это форк pion или замена ICE-агента. Минусы перевешивают плюсы: TCP-в-TCP даёт меньше throughput, чем UDP-в-TCP, на лоссливых каналах.

**Я НЕ рекомендую путь (b)**, разве что мы прижаты к стене.

---

## 3. Поэтапный план (предложение к утверждению)

### Фаза 0 — Подготовка ветки (этот шаг УЖЕ сделан)

- [x] Чтение всех документов в `docs/`.
- [x] Откат рабочей ветки до `fb37254`. Текущая ветка: `feat/transport-rework`.
- [x] Создание этого плана и аудита.

### Фаза 1 — Очистка и фиксы lifecycle (НЕ затрагивает архитектуру)

Цель: устранить классы багов, которые портят бенчмарки и провоцируют «обрывы независимо от транспорта».

| # | Задача | Файл | Сложность | Риск |
|---|--------|------|-----------|------|
| 1.1 | `socksConn` — atomic `closed`, idempotent `stopAppWriter`, `deliverToApp` через `select { case toApp <- p; case <-done: }`, удалить `sync.Once` для startAppWriter | `tunnel/relay_bridge.go` | M | low |
| 1.2 | `closeJoinerAfterInboundDrain` — сначала Delete из map, потом close conn, потом close chan (порядок!) | `tunnel/relay_bridge.go` | S | low |
| 1.3 | `closeDCConn` — `sync.Once` вместо `defer recover()`, чёткий порядок | `creator/session.go` | S | low |
| 1.4 | `connectTCP` (server) — half-close: `conn.(*net.TCPConn).CloseWrite()` на EOF, ждать `MsgClose` от joiner или таймаут drain, потом full close | `creator/session.go`, `tunnel/relay_bridge.go` (creator branch) | M | medium |
| 1.5 | `dcConn.inCh` — убрать silent drop. Вариант: блокирующий `inCh <-`, увеличить буфер до 1024. Альтернатива: писать прямо в `conn` под мьютексом | `creator/session.go` | S | medium (может затормозить SCTP reader) |
| 1.6 | `relayTCP` — добавить `tx` counter, чтобы `closeRelayTCP` логировал реальный tx | `tunnel/relay_bridge.go` | XS | none |
| 1.7 | Убрать абстракционные ляпы (`if _, ok := rb.tunnel.(OutboundQueued); ok` повторяется) — вынести в `DataTunnel` interface | `tunnel/protocol.go`, `tunnel/outbound_drain.go` | S | none |
| 1.8 | Логи: добавить счётчик «VP8 reassembler reset» — для измерения частоты потерь в фазе 2 | `tunnel/vp8read.go` | XS | none |

**Принцип:** фаза 1 — это **строго багфиксы и наблюдаемость**, без архитектурных изменений. Можно мерджить и катить отдельно.

**Acceptance criteria фазы 1:**

- Нет паник в стресс-тесте (100 параллельных curl'ов).
- В логах виден `tx>0 rx>0` на закрытии соединений.
- `vp8 reassembler reset` метрика в логах работает.

### Фаза 2 — Замер DC vs VP8 в relay-only

Цель: получить **данные**, на которых принимается решение D1.

| # | Сценарий | Метрика | Порог |
|---|----------|---------|-------|
| 2.1 | curl 92 MB через VP8 mode, `policy=relay` | % завершения, Mbps avg, Mbps max | baseline |
| 2.2 | curl 92 MB через DC mode, `policy=relay` | то же | сравниваем с 2.1 |
| 2.3 | Если DC mode `≥ 95 %` и Mbps ≥ 50 % от VP8 | **D1 = Path A**, фаза 3 = только default mode toggle | — |
| 2.4 | Если DC mode `< 95 %` или Mbps `< 50 %` от VP8 | **D1 = Path B**, фаза 3 = ARQ или KCP | — |

Бенчмарки записываются в `bench-results/relay_dc_vs_vp8_<дата>.txt`.

**Замер требует поднятого сервера + клиентского запуска.** Сам ИИ-агент это не делает автоматически — нужен пользователь (или скрипт под `scripts/`). Заранее подготовим скрипт `scripts/bench-dc-vs-vp8.ps1`.

### Фаза 3a — Если выбран Path A (DC default)

| # | Задача | Файл | Сложность |
|---|--------|------|-----------|
| 3a.1 | Изменить default `tunnelMode` на `dc` когда `policy=relay` | `vk-vpn-client/webrtc/joiner.go::NewJoiner` | XS |
| 3a.2 | Документировать в README/USER_GUIDE: relay-only ⇒ DC bulk | `README.md`, `USER_GUIDE_RU.md` | XS |
| 3a.3 | Дать env-override `VK_VPN_TUNNEL_MODE=video` для исследовательских целей (уже есть, не менять) | — | — |
| 3a.4 | Smoke-tests: 92 MB curl × 5 итераций, целевое: 100 % все 5 раз | scripts | S |

**Это всё.** Никакого ARQ, никакого FEC. SCTP делает свою работу.

### Фаза 3b — Если выбран Path B (KCP-over-VP8)

Только если **Фаза 2 показала**, что DC mode непригоден.

| # | Задача | Файл | Сложность | Риск |
|---|--------|------|-----------|------|
| 3b.1 | Подключить `github.com/xtaci/kcp-go/v5` (или эквивалент) | `go.mod` | XS | low |
| 3b.2 | Обернуть `VP8DataTunnel` в `kcp.UDPSession`-подобный wrapper: на send — kcp.Output → VP8 sample, на recv — VP8 frame → kcp.Input | `tunnel/kcp_over_vp8.go` (new) | L | medium |
| 3b.3 | На уровне `RelayBridge` остаётся та же frame-структура, только она течёт через kcp.Stream вместо raw VP8 sendQueue | `tunnel/relay_bridge.go` | M | low |
| 3b.4 | Настройка KCP: `NoDelay(1, 10, 2, 1)` (fast mode), window 1024×1024, MTU 1300 (запас под VP8-обфускацию + DTLS overhead) | `tunnel/kcp_over_vp8.go` | M | medium |
| 3b.5 | Бенчмарк kcp-over-VP8 vs DC vs raw VP8 | scripts | S | — |

**KCP даёт:**
- ARQ (Selective Repeat) — корректный, не наш самопал;
- опциональный FEC;
- congestion control (опционально);
- streaming/datagram режим;
- не требует отдельной ACK-plane через DC.

**Минусы KCP:**
- ~10-20 % overhead vs raw;
- меньше «настраиваемых ручек» чем у самопала, но больше тестового опыта.

### Фаза 3c — НЕ рекомендую, но оставляю на крайний случай

Свой sliding-window ARQ по `ARCHITECTURE_AUDIT_ARQ.md`. Реализуем **только** если и DC, и KCP отвергнуты. Декомпозиция, риски, ловушки — см. оригинальный документ + добавки: явные unit-тесты на seq wrap, replay, deadlock, cache eviction.

### Фаза 4 — Verification (после реализации, до отчёта)

- curl 92 MB × 10 итераций — все 10 на 100 %.
- speedtest стабильно (без курса в 0 kbps).
- Long-running: 1 час непрерывной нагрузки без обрывов.
- Stress: 50 параллельных curl'ов.
- Edge: peer restart посередине download'а → восстановление без потери данных (опционально, для KCP path).

### Фаза 5 — Implementation report

Создаётся **только после** Фазы 4 и **только по команде пользователя**. Файл `docs/TRANSPORT_FIX_REPORT.md` опишет: что сделано, какие компромиссы, какие метрики до/после.

---

## 4. Риски, компромиссы, ограничения

### Что я НЕ гарантирую

- **Что DC mode в relay сразу даст ≥ 18 Mbps.** Это надо мерить. Если SCTP-CC консервативен на 100ms+ RTT, может быть 5-10 Mbps. Тогда Path B становится оправдан.
- **Что вы получите 100 % доставки в VP8 mode без переделки**. VP8 без ARQ/jitter — лотерея. Любая работа над «улучшением VP8» без app-level reliability — игра с симптомами, не лечение.
- **Что фикс half-close не вскроет латентных багов в pion**. Pion обычно справляется, но half-close path тестируется реже.

### Что я считаю компромиссами, которые нужно осознанно принять

| Компромисс | Цена | Выгода |
|------------|------|--------|
| Отказ от VP8 как default | теряем «звонок выглядит активным» во вкладке VK (для авторитета звонка) | надёжность 100 %, проще код |
| Backpressure в `inCh` вместо drop | upstream может замедлиться при busy origin | нет тихой потери данных |
| Half-close drain до 120 s | дольше освобождаются ресурсы соединений | хвост 99 % файла не теряется |
| Подключение KCP-go (если Path B) | +зависимость, +overhead, +1 уровень кода | надёжный ARQ без своего велосипеда |

### Что я считаю «не делать»

- **Кастомный TCP через TURN-TCP** (forking pion-ice). Соотношение цена/выгода катастрофическое.
- **Аудио-канал для bulk** — bitrate-cap обнулит идею (см. `THROUGHPUT_HYPOTHESES.md` D2).
- **Второй VP8-track / bonding** — это про повышение скорости, не про надёжность. Сначала надёжность.
- **«Подкрутить TCP параметры на VPS»** — путь к origin полностью надёжен, проблема не там.
- **Жить с silent drop в `inCh`.** Однозначно нет.

---

## 5. Что прошу утвердить **до** реализации

1. **D1 (выбор пути).** Согласны ли с порядком: «Сначала измерить DC vs VP8 в relay, потом решать»? Или хотите сразу Path B / Path C, **минуя замер**?
2. **D2.** Согласны ли начать с **Фазы 1 (cleanup/bugfix) до архитектурного решения**? Или хотите параллельно (рискованно для бенчмарка) или после (баги исказят замер)?
3. **D3.** Оставляем VP8 mode как legacy fallback (`VK_VPN_TUNNEL_MODE=video` для исследования)?
4. **D4.** Если Path B, готовы добавить `github.com/xtaci/kcp-go/v5` в зависимости?

Только после явного «ок» на эти 4 пункта я перехожу к коду. Сейчас текущая ветка `feat/transport-rework` чистая, можно вносить любые правки.

---

## 6. Структура коммитов (заранее, чтобы план был обозримый)

Если утверждаете порядок Фаза 1 → Фаза 2 → Фаза 3a (Path A), то ветка будет:

```
fb37254  last commit cursor (база)
   ├─ feat/transport-rework
   │
   ├─ fix(socksConn): close-race free lifecycle (1.1, 1.2)
   ├─ fix(session): sync.Once for closeDCConn (1.3)
   ├─ feat(creator): TCP half-close on origin EOF (1.4)
   ├─ fix(session): backpressure for dcConn.inCh (1.5)
   ├─ feat(diag): tx counter + vp8 reset metric (1.6, 1.8)
   ├─ refactor(tunnel): clean OutboundQueued (1.7)
   ├─ bench(dc-vs-vp8): scripted comparison (Фаза 2)
   ├─ feat(joiner): default mode=dc when policy=relay (3a.1)
   ├─ docs: TRANSPORT_AUDIT, TRANSPORT_FIX_PLAN updates
   └─ docs: TRANSPORT_FIX_REPORT (по команде)
```

Если Path B — после bench-commit добавится серия kcp-related коммитов.

---

## 7. Резюме для пользователя (1 абзац)

В коде сейчас два источника «обрывов»: (1) **VP8-канал не имеет надёжной доставки** — любая UDP-потеря портит чанк TCP-данных, отсюда curl 84 % и connection reset; (2) **lifecycle TCP-соединений содержит реальные race'ы** (`socksConn.toApp` send-on-closed, server `inCh` silent drop, отсутствие half-close), которые рвут соединения **независимо** от UDP-потерь. Предложение `ARCHITECTURE_AUDIT_ARQ.md` решает только (1), и решает дорого — реализуя TCP-Lite поверх VP8, в то время как **в режиме relay у нас уже есть надёжный SCTP DataChannel «из коробки»**. Мой план: сначала чинить (2), потом измерить, насколько SCTP DC в relay медленнее/быстрее VP8, и принять архитектурное решение **на данных, а не на догадках**. Если DC хватит — закрываем эту тему без всякого ARQ. Если нет — берём KCP-go (3k LoC, продакшен-проверенный), а не пишем свой sliding window в пятый раз.
