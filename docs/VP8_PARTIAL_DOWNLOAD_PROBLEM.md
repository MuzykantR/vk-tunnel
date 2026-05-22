# Проблема: «файл на VPS целиком, у клиента 50–85%» (VP8 / UDP)

> Документ для следующего агента. **Не переизобретать диагноз** — сначала сверить симптом с этой моделью, потом менять код по приоритету ниже.

Связано: [THROUGHPUT_HYPOTHESES.md](./THROUGHPUT_HYPOTHESES.md) (гипотезы B*, C*, D*), [THROUGHPUT_PLAN.md](./THROUGHPUT_PLAN.md) (фазы), [RELAY_BENCHMARK.md](../vk-vpn/docs/RELAY_BENCHMARK.md) (ICE relay-тест), [ICE_PION_AUTO_RESTORE.md](../vk-vpn/docs/ICE_PION_AUTO_RESTORE.md) (pion auto vs TURN-only).

---

## 1. Формулировка проблемы (одним абзацем)

**Симптом:** `curl` большого файла через VPN останавливается на 36–84% (~12 Mbps), страницы «догружаются наполовину».

**Суть:** На VPS TCP до origin **надёжный** — в `serverlog` `relay: close … 213.180.204.183 rx=96331062` (полный объём). До пользователя байты идут **вторым транспортом** — один RTP-поток «фейкового» VP8 внутри звонка VK (**UDP**, policer SFU, pacing 24×30). Потери и throttle на этом UDP-участке **не компенсируются** TCP на стороне curl: SOCKS/TCP ждёт байты, которых в VP8-кадрах уже нет.

**Это не:** «сломанный relay TCP на сервере», «idle = сервер рвёт соединение», «curl баг».

**Это:** ограничение **ёмкости и надёжности одного VP8-канала** + путь ICE (direct быстрее, TURN нестабильнее) + конкуренция сотен мелких HTTPS через тот же поток.

---

## 2. Доказательная база (что уже видели в логах)

| Наблюдение | Где | Вывод |
|------------|-----|--------|
| `rx=96331062` на `213.180.204.183` | server `relay: close id=…` | Origin→VPS **100%** |
| curl 77.5 MB / 84% | клиент | Joiner→ПК **не 100%** |
| Пик `vp8: recv` до #8700+, потом пауза 20–40 с | `app.log` ~07:44:09+ | Затык **входящего** VP8, не обязательно ICE down |
| ICE `relay` only, `watchdog missed pongs`, restart suppressed | `app.log` 23:58 | На TURN recovery слабый |
| ICE `host/srflx`, 84% без disconnect | сессия 07:42–07:44 | Путь лучше, проблема **частично** остаётся (B2/B1) |
| SSH broken pipe при relay-only без bypass VPS | практика | **Маршрутизация**, не VP8; лечится `VK_VPN_EXTRA_BYPASS_IPS` |

**Правило для агента:** перед правками VP8 сравнить **размер curl** и **`rx` в serverlog** для того же `id` и времени.

---

## 3. Стек (куда именно теряются байты)

```text
[yandex] --TCP reliable--> [VPS relay TCP rx=FULL]
                              |
                    encode → VP8 samples → RTP/UDP (WebRTC)
                              |
                    VK SFU / TURN (UDP, policer, jitter)
                              |
                    decode ← VP8 ← [joiner SOCKS → curl TCP]
                              |
                    пользователь видит 50–85%
```

| Узел | Протокол | Можно ли «докачать» TCP? |
|------|----------|---------------------------|
| VPS ↔ mirror | TCP | Да (уже полный rx) |
| VPS → клиент (туннель) | **UDP / VP8** | **Нет** — нет app-level retransmit |
| curl | TCP | Ждёт то, что отдал SOCKS |

---

## 4. Что делать — рычаги по приоритету

### P0 — измерения (без кода)

1. Одна сессия: curl до конца, **не останавливать** вручную на 84%.
2. Сверить `rx` на сервере с `%{size_download}` curl.
3. Записать `ICE selected` (relay vs host/srflx), длительность активного `vp8: recv`.
4. Тест **без** фоновых вкладок (B3 — десятки `relay: close` на Google).

### P1 — env (дешево, сегодня)

| Рычаг | Env | Ожидание | Риск |
|-------|-----|----------|------|
| Pacing | `VK_VPN_VP8_FPS=30` `VK_VPN_VP8_BATCH=40`…`50` | +sustained Mbps, ближе к 100% curl | disconnect / ban SFU |
| Профиль | `VK_VPN_VP8_PROFILE=aggressive` (=30×50) | то же | то же |
| Путь ICE дома | `VK_VPN_ICE_TRANSPORT_POLICY=all` | **Самый большой** win стабильности/Mbps | не whitelist-тест |
| Путь ICE вуз | `relay` (default в коде) + `VK_VPN_EXTRA_BYPASS_IPS=<VPS>` | стабильность admin SSH | медленнее |
| Хвост файла | `VK_VPN_RELAY_INBOUND_GRACE_SEC=10` (default уже 10) | меньше обрыв после MsgClose | — |
| Drain на сервере | `VK_VPN_RELAY_DRAIN_TIMEOUT=120` | MsgClose после опустошения sendQueue | — |

**Играть с FPS/batch — да, это первый эксперимент** (гипотезы C, B1). Формула WLB: `throughput ≈ fps × batch × ~1126 B` на sample (потолок теории; VK может резать раньше).

### P2 — код (уже частично / в плане)

| Задача | Файл | Статус | Зачем |
|--------|------|--------|-------|
| Burst drain sendQueue | `tunnel/vp8tunnel.go` | **есть** (`maxBurst = batch*8`) | не простаивать при backlog |
| Non-blocking / coalesce SendData | `vp8tunnel.go` | **план фаза 1** | B1 — SOCKS не блокирует encoder |
| Async write SOCKS ← VP8 | `tunnel/relay_bridge.go` | **сделано** (`toApp` queue 512) | curl slow не блокирует recv chain |
| Лимит parallel CONNECT | `VK_VPN_RELAY_CONNECT_LIMIT=32` | env | B3 — меньше очереди на одном VP8 |
| Второй VP8 track (screen) | joiner + creator | **фаза 3** | D1/D3 — ×1.5–2 Mbps если SFU не режет |
| FEC / reorder buffer на joiner | — | **не начато** | вуз с потерями UDP; сложно |

### P3 — не поможет или ложный след

| Идея | Почему нет |
|------|------------|
| «Подкрутить TCP на relay» | До VPS TCP уже OK |
| «Ждать idle pong» на SOCKS | Idle не рвёт origin; рвёт VP8/ICE |
| Audio track для bulk | Bitrate cap ~128 kbps |
| Ещё ICE restart на relay | Подавлено намеренно; ухудшает |
| Ожидать 100 Mbps на одном VP8 | Прод verify ~18 Mbps sustained, пик ~50 |

---

## 5. Ответы на «с чем играть»

### FPS и batch

- **Да.** Увеличивают **число байт в единицу времени** в одном потоке (если SFU пускает).
- Начать: `30×40`, потом `30×50`; смотреть disconnect и `vp8: recv` без 30-с пауз.
- Код: `vk-vpn-client/webrtc/vp8_env.go`, sync joiner→creator через `MsgConfig` в `relay_bridge.go`.

### Очередь (sendQueue)

- Сейчас **4096** слотов, blocking `SendData` — при перегрузе **замирает** чтение origin на VPS (B1).
- План: non-blocking + метрика `sendQueue full`, опционально `VK_VPN_VP8_SEND_QUEUE=2048` A/B.
- На **приёме** — отдельная очередь `socksConn.toApp` (512) уже развязывает curl от VP8.

### «Поток» (второй канал)

- **Один VP8 = один UDP RTP поток** — главный потолок.
- Реальный следующий шаг для **100% больших файлов** — **второй video/screen track** (фаза 3) или bonding; не «ещё fps» бесконечно.

### ICE (не VP8, но сильнее для «дыр»)

- **Direct** (pion auto): меньше потерь, выше Mbps → меньше дыр в curl.
- **Relay-only** (тест whitelist): ожидайте **хуже** partial download дома — это норма теста, не регрессия relay bridge.

---

## 6. Критерии «проблема решена» / «упёрлись в потолок»

| Критерий | Порог |
|----------|--------|
| curl 92 MB файл | `size_download` ≥ 95% размера, `rx` на сервере ≈ то же |
| Стабильность | нет паузы `vp8: recv` >10 s mid-download без ICE down |
| Throughput | sustained ≥22 Mbps дома **или** доказан лимит линии (speedtest без VPN) |

Если при **direct ICE + 30×50 + async SOCKS** всё ещё 80–85% с полным `rx` на VPS — потолок **VK SFU** (B2) → только **второй track** или смириться с ~6–15 Mbps на один звонок.

---

## 7. Чеклист для агента (порядок работ)

1. [ ] Подтвердить split: server `rx` full vs client partial (одна сессия).
2. [ ] A/B ICE: `all` vs `relay` на одном файле curl.
3. [ ] A/B VP8: 24×30 → 30×40 → 30×50 (записать % и Mbps).
4. [ ] Один curl, без браузера; `EXTRA_BYPASS_IPS` для VPS при relay.
5. [ ] Если всё ещё partial при full rx — фаза 1.1 vp8tunnel + фаза 3 dual VP8 (см. plan).
6. [ ] Не тратить время на «TCP до клиента» — его нет на медиа-пути.

---

## 8. Ключевые файлы

| Файл | Роль |
|------|------|
| `vk-vpn-client/tunnel/vp8tunnel.go` | sendQueue, writerLoop, burst |
| `vk-vpn-client/tunnel/relay_bridge.go` | SOCKS, inbound grace, `toApp` |
| `vk-vpn-server/creator/session.go` | origin TCP, drain перед MsgClose |
| `vk-vpn-client/webrtc/vp8_env.go` | FPS/batch env |
| `vk-vpn-client/tunnel/relay_env.go` | grace, drain, connect limit |
| `vk-vpn-client/webrtc/ice_settings.go` | relay vs all |
| `vk-client/main.go` | redirect, bypass, `EXTRA_BYPASS_IPS` |

---

## 9. Одна фраза для PR / пользователя

**«VPN не “теряет TCP до сайта” — VPS скачивает файл полностью; не доезжает UDP-тупик звонка (VP8). Лечим pacing, очереди, путь ICE и при необходимости второй VP8-track; не ожидаем поведения провода Ethernet внутри WebRTC.”**
