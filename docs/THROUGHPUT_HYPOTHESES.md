# Максимум скорости и стабильности: карта гипотез

> Документ для следующего AI-агента. Контекст проекта: **vk-vpn** — VPN через WebRTC (VK Call), референс **whitelist-bypass** (`c:\VPN\whitelist-bypass`).  
> Прод-верификация (20.05.2026): Speedtest **DL 18.28 / UL 17.37 Mbps**, ping **107 ms** (jitter до 351 ms), пик ~50 Mbps → спад. Режим **VP8 video** (`VK_VPN_TUNNEL_MODE=video`), pacing **24×30**.  
> На **сети вуза** почти не работало; дома Wi‑Fi «максимум» ~18 Mbps — пользователь хочет больше.  
> Формула WLB: `throughput ≈ fps × batch × ~1126 B` на sample.

---

## 0. Главный вывод заранее

**18 Mbps на домашнем Wi‑Fi — скорее всего не «потолок Wi‑Fi», а потолок одного VK‑звонка как транспорта** (политика SFU/TURN + один RTP‑поток + pacing + конкуренция сотен TCP).

**Пик ~50 Mbps** доказывает: канал **умеет** больше, но что‑то **душит sustained** (scheduler, burst‑политика, jitter, очередь на joiner, relay, или лимит pacing).

**Вуз** — отдельная вселенная: там чаще ломается **стабильность пути** (UDP, TURN, NAT, DPI), а не VP8 pacing.

---

## 1. Физическая модель: где биты теряются

```text
[Сайт] ←TCP→ [tun2socks] ←SOCKS→ [VP8 encoder] ←RTP/UDP→ [VK TURN?] ←→ [VPS relay] ←TCP→ [интернет]
         ↑                    ↑              ↑                    ↑
      MTU 1400            1 очередь      fps×batch           RTT 107ms
      много TCP           sendQueue      keepalive           BDP ~650KB @ 50Mbps
```

| Узкое место | Симптом | Оценка вклада в «18 vs 50» |
|-------------|---------|----------------------------|
| **Один VP8‑поток** | Пик 50, sustained 18 | **Высокий** — один «трубопровод» |
| **TURN relay + RTT** | ping 107 ms | **Высокий** для TCP внутри туннеля |
| **Pacing 24×30** | Теор. ~8–16 Mbps по формуле WLB | **Средний** — уже выше формулы → SFU иногда пускает больше |
| **Домашний uplink** | UL 17 Mbps | **Средний** — speedtest symmetric → uplink может быть лимитом DL теста |
| **Obfuscation ChaCha20** | ~десятки Mbps CPU на слабом ПК | **Низкий–средний** |
| **tun2socks + SOCKS framing** | Overhead на мелких пакетах | **Низкий** при крупных transfer |
| **VK SFU policer** | Неизвестный cap на «video» | **Высокий** (чёрный ящик) |

---

## 2. Почему дома «максимум 18» — гипотезы (от проверяемых к смелым)

### A. Путь ICE (сеть, не код)

| # | Гипотеза | Механизм | Как проверить | Потенциал |
|---|----------|----------|---------------|-----------|
| A1 | Всегда **relay**, не host/srflx | TURN добавляет RTT×2 и policer | Лог `ICE selected … relay` vs `host/srflx` | **+30–100%** если получится direct |
| A2 | **Uplink** дома = 20 Mbps | Speedtest UL 17 → DL не может долго держать 50 | Тест с Ethernet к роутеру, другой провайдер | До **линии** |
| A3 | **Bufferbloat** на Wi‑Fi | Пинг 107 + jitter 351 под нагрузкой | `ping` во время speedtest; FQ_CoDEL на роутере | Стабильность + **+10–30%** sustained |
| A4 | **MTU 1400** vs 1500 (WLB) | Больше фрагментации → больше conn для того же объёма | A/B `1500` на tun + gvisor | **+3–8%** |
| A5 | VPS egress или NIC cap | Один VPS, shared DC | `iperf3` с VPS наружу без VPN | Потолок **сервера** |

### B. Один канал VP8 (механика, как DC раньше)

| # | Гипотеза | Механизм | Проверить | Потенциал |
|---|----------|----------|-----------|-----------|
| B1 | **Pacing** не выжимает pipe | `writerLoop` шлёт keepalive в idle ticks вместо данных; `sendQueue` 1024 блокируется | Профиль: `sent #N size=`, batch 40–60, fps 30 | **+20–40%** sustained |
| B2 | **Пик 50** = краткий burst, потом **token bucket** VK | SFU режет после 2–5 с | Длинный iperf 60 с, график Mbps каждую секунду | Объясняет форму, не лечит |
| B3 | **Много TCP** через один VP8 → внутренняя очередь SOCKS | 48-й CONNECT при «медленной» странице | Число активных `relay: CONNECT`, сравнить с одним curl | Стабильность UX |
| B4 | Крупные кадры **редко** в логе | Лог только #1–5 и каждый 500-й — при 18 Mbps кадры KB+ есть, но редко логируются | Временно логировать size>4KB | Диагностика |
| B5 | **Обратный** канал (ACK/data) тоже VP8 | Full duplex на одном track — upload speedtest грузит оба направления | Тест только DL (curl одного файла) vs speedtest | Разделить лимиты |

### C. Параметры VP8 (самый дешёвый эксперимент)

Формула WLB: при **30 fps × 50 batch** теоретический потолок framing выше; уже **18 при 24×30** → запас есть, но VK может **throttle**.

| Параметр | Риск | Ожидание |
|----------|------|----------|
| `VK_VPN_VP8_BATCH=50` | CPU, SFU ban | **20–35 Mbps** sustained или disconnect |
| `VK_VPN_VP8_FPS=30` | Больше RTP overhead | **+10–20%** |
| Оба | Агрессивно | Пик ближе к 50 дольше; стабильность под вопросом |

**Env-переменные:** `VK_VPN_VP8_FPS`, `VK_VPN_VP8_BATCH`, `VK_VPN_TUNNEL_MODE=video|dc`, `VK_VPN_ICE_RELAY_WAIT`, `VK_VPN_LOG_LEVEL`.

### D. «Ещё техники как VP8» (новые носители в том же звонке)

Исторический контекст: проект сначала **не использовал VP8**, додумались из **whitelist-bypass** — возможны другие носители в том же WebRTC-звонке.

| # | Идея | Суть | Реалистичность Mbps | Стабильность |
|---|------|------|---------------------|--------------|
| D1 | **Второй video track (screen share)** | У VK часто отдельный m-line / отдельная очередь SFU | **×1.3–2?** (эксперимент) | Средняя |
| D2 | **Opus/audio как data tunnel** | WLB в Telemost называет track `tunnel-audio`, но VK mode = VP8; Opus обычно **<512 kbps** номинально | **Низкая** для bulk | Высокая для antifraud |
| D3 | **Dual VP8** (2 video tracks) | 2 независимых RTP → 2 логических туннеля + bonding на клиенте | **×1.5–2** если SFU не режет | Сложно |
| D4 | **DC + VP8 одновременно** | Split: control на DC, bulk на VP8 — или наоборот | Зависит от лимита DC у VK | Средняя |
| D5 | **Unordered DC** для части трафика | Второй negotiated DC, unreliable (roadmap фаза 5) | QUIC-like; **средняя** для UDP/DNS | Высокая для jitter |
| D6 | **H264/AV1** вместо VP8 | Если VK предпочитает H264 — лучше compression | **+10–30%**? | Нужен codec probe |
| D7 | **Меньше obfuscation** на hot path | ChaCha20 на каждый sample — CPU latency | **+5–15%** на слабом CPU | — |
| D8 | **Raw RTP без «fake VP8»** | Если SFU не валидирует payload глубоко (риск бана) | Теоретически max | **Антифрод** |

**Про audio для данных:** как **основной** канал — почти наверняка **хуже VP8** (bitrate cap + codec designed for 20–128 kbps). Как **второй канал** для **части** трафика (DNS, ACK, keepalive) или **antifraud** (comfort noise) — другая ось. WLB для VK выбрал VP8 потому, что SFU **ожидает video bitrate**.

**Самая «VP8-like» находка следующего уровня:** не audio, а **второй high-bitrate track** (screen share / second video) + **bonding на joiner**.

**Текущий код (справка):** tunnel DC — negotiated id=2, **ordered + reliable**, Detach() read-loop (как WLB); основной трафик в video mode — **VP8**, не DC. Файлы: `vk-vpn-client/webrtc/joiner.go`, `vk-vpn-client/tunnel/vp8tunnel.go`, `vk-vpn-server/creator/session.go`, `vk-client/main.go` (ICE stable → default route), `vk-client/wintun/` MTU **1400**.

### E. Несколько звонков (bonding)

| Вариант | Mbps | Сложность | VK risk |
|---------|------|-----------|---------|
| 2 звонка, 2 SOCKS, round-robin TCP | **~1.4–1.8×** | Очень высокая | Высокий |
| 2 звонка, split DL/UL | **~1.2–1.5×** | Экстремальная | Высокий |
| 2 VPS в разных AS | **Латентность↓** | Инфра | Средний |

Имеет смысл только **после** выжима одного звонка до **30+ Mbps** и логов `ICE selected`.

### F. Стабильность (вуз vs дом)

| # | Гипотеза (вуз) | Почему «не грузится» |
|---|----------------|----------------------|
| F1 | **UDP/TURN blocked** или rate-limited | ICE connect, потом 0 B |
| F2 | **Symmetric NAT + hairpin** | Работает дома, ломается в общежитии |
| F3 | **Captive portal / фильтр** | Режет non-443 UDP |
| F4 | **Wi‑Fi airtime** 200+ студентов | Потери → VP8 без FEC → stall |
| F5 | **Неполный bypass** (новые TURN IP) | ICE через туннель → pet loop |
| F6 | **MTU black hole** | 1400 + PPPoE вуза |
| F7 | **DNS через туннель** медленный | Страница «висит», кажется VPN мёртв |

**Адаптивный режим (продукт):** детект «плохая сеть» → увеличить keepalive, снизить parallel SOCKS, `ICE_RELAY_WAIT=0` + TURN-TCP only, временно снизить batch, не ставить default route до stable 5 s.

**Уже сделано в коде (не дублировать без нужды):** убран 6 s fallback route; `WaitIceStable` 3 s + flush bypass + sleep 2 s; `AddBypassFromCandidate`; ICE restart suppress 45 s, grace 12 s; VP8 default; SCTP 8 MB, DetachDataChannels; RelayBridge; watchdog ping/pong.

---

## 3. Связка «дом 18» vs «вуз плохо»

```text
                    ДОМА                          В УЗЕ
Путь ICE:     relay 107ms (работает)     relay/flap или block UDP
VP8:          18 Mbps sustained          мало кадров / disconnect
Wi‑Fi:        bufferbloat?              congestion + loss
Код:          OK (prod verify)           bypass/TURN/new IP критичнее
```

**Вывод:** вуз лечится **стабильностью пути** (F*), дом — **ёмкостью одного VP8** (B*, C*, D*).

---

## 4. План атаки по приоритету (максимум Mbps / стабильность)

### Фаза измерений (1–2 вечера, без большого кода)

1. Зафиксировать **`ICE selected`** на домашнем Wi‑Fi и в вузе (если возможно).
2. График **Mbps каждые 2 с** на 60 с (не одна цифра speedtest).
3. Тест **только download** (один `curl -o NUL` большого файла) vs speedtest.
4. `VK_VPN_VP8_BATCH=40` и `50` — по 5 минут каждый, записать max/avg и ICE.

### Фаза быстрых wins (код/конфиг)

| Действие | Цель Mbps | Цель stability |
|----------|-----------|----------------|
| batch/fps sweep | **20–35** | watch disconnect |
| MTU 1500 A/B | +несколько % | меньше frag |
| Лог `ICE selected` + warn если relay | direct path | — |
| TURN-TCP fallback flag в плохих сетях | вуз | latency↑ |
| Расширить bypass (все TURN IP из connection notify) | вуз | ICE не flap |

### Фаза «следующий VP8» (исследование)

1. **Screen-share track** как второй `VP8DataTunnel` — зеркало открытия VP8 (как раньше без VP8 → whitelist-bypass).
2. **Два звонка / два SOCKS** — только benchmark, не продукт.
3. **Профилирование sendQueue** — не терять тики на keepalive при backlog.

### Фаза «не для Mbps»

- Comfort audio (antifraud) — **стабильность звонка**, не +Mbps.
- S3 / VK bot — продукт.

---

## 5. Ответ на «18 мало, на Wi‑Fi только столько»

Три взаимоисключающие интерпретации — **все три стоит проверить**:

1. **Линия дома** реально ~20 Mbps up (speedtest UL 17) → **18 — почти потолок линии**, VP8 ни при чём.
2. **Линия 100+ Mbps**, но **VK tunnel ~18** → копаем B/C/D (pacing, relay, SFU).
3. **Линия 100+**, **пик 50** → tunnel **умеет больше**; нужен sustained scheduler (B1, batch), не новый codec.

**Один решающий тест:** Ethernet к роутеру + speedtest **без VPN** и **с VPN**.

---

## 6. Радикальные гипотезы (дальняя полка)

- **Не маскироваться под VP8**, а под **H264 screen share** с реальным keyframe rate — если VK даёт больше bitrate на «демонстрацию экрана».
- **FEC / redundant encoding** на RTP (если pion + VK не ломают) — вуз с потерями.
- **Клиент ближе к VPS** (joiner на RU edge, не Stockholm) — RTT 30 ms → TCP внутри туннеля **×1.5–2** effective.
- **Выделенный TURN** нельзя; но **direct UDP** к VPS — game changer.
- **Split-tunnel**: только «заблокированные» префиксы через VPN, остальное мимо — скорость ощущения для пользователя (не full tunnel Mbps).

---

## 7. Рекомендуемый порядок работ для агента

**Если цель — выжать максимум из одного звонка:**

1. **ICE host/srflx** (логи + bypass + relay wait) — самый большой рычаг.
2. **VP8 batch 40–60 + fps 30** — дешёво, может дать **20–30+** или показать потолок SFU.
3. **Screen-share второй track** — единственная «новая механика» с шансом как VP8, не audio.
4. **Понять uplink дома** — иначе оптимизируете VK, а лимит — роутер.

**Если цель — вуз:**

1. TURN-TCP, агрессивный bypass, adaptive «плохая сеть», не гнать 50 parallel TCP.

**Audio для передачи файлов** — скорее **ложный след**; audio — для **правдоподобия звонка** и, в лучшем случае, **килобитного control**, не для конкуренции с VP8.

---

## 8. Связанные файлы и документы

| Путь | Роль |
|------|------|
| `docs/BENCHMARKS.md` | Результаты speedtest, env |
| `docs/WLB_GAP_ANALYSIS.md` | Отличия от whitelist-bypass |
| `WLB_INTEGRATION.md` | Интеграция WLB-паттернов |
| `implementation_plan.md` | План фаз |
| `roadmap.md` | Backlog (S3, comfort audio, MTU A/B) |
| `docs/LOGGING.md` | `VK_VPN_LOG_LEVEL`, теги |
| `whitelist-bypass/` | Референс VP8 pacing, bypass до WebRTC |

**Ключевой прод-лог:** `[creator] === MODE: VIDEO (VP8) ===`, `fps=24 batch=30`, `[video] recv vp8 frame #N XX bytes` (24–74 B = keepalive/MsgConfig, KB+ = трафик).

**Speedtest ID (прод):** 19213862301.

---

## 9. Чеклист экспериментов (заполнять агенту)

**Фаза 0:** протокол [BENCHMARK_PROTOCOL.md](BENCHMARK_PROTOCOL.md), шаблон `scripts/bench-notes.ps1`.

| # | Эксперимент | Env / действие | DL Mbps avg | DL max | UL | ICE path | Примечания |
|---|-------------|----------------|-------------|--------|-----|----------|------------|
| 0 | Без VPN (линия) | Яндекс Интернетометр / 72.45 DL | 72.45 | — | 70.82 | n/a | 20.05 фаза 0 ✅ линия быстрая |
| 1 | Baseline VPN | video 24×30, Ookla | ~15 | ~60 spike | 40 | **host/host** | 20.05 фаза 0 ✅ |
| 1b | curl DL ~92 MB | mirror.yandex.ru | ~8.0 | — | — | host/host | **curl (18)** @96%, tail stall |
| 1c | Прод speedtest | video 24×30 | 18.28 | ~50 spike | 17.37 | relay? | 20.05 ранний прогон |
| 2 | batch=40 | `VK_VPN_VP8_BATCH=40` | | | | | фаза 1 |
| 3 | batch=50 | `VK_VPN_VP8_BATCH=50` | | | | | |
| 4 | fps=30 | `VK_VPN_VP8_FPS=30` | | | | | |
| 5 | DL only curl | один большой файл | | | | | vs speedtest |
| 6 | MTU 1500 | tun wintun | | | | | |
| 8 | Ethernet no VPN | опционально | | | | | уточнить Wi‑Fi vs LAN |

---

*Создано по запросу пользователя: зафиксировать полный стратегический разбор гипотез для следующего AI-агента.*
