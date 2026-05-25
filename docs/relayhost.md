Сводка по документам, бенчмаркам и чатам ([1f25fd3c-0e84-4be7-8a82-e9c3a42eb454](1f25fd3c-0e84-4be7-8a82-e9c3a42eb454)). Ниже — **пик в моменте** (не sustained), по сути **скорость туннеля поверх UDP/WebRTC**, не «чистый iperf UDP».

---

## HOST (ICE direct: host / srflx / prflx, без TURN в nominated pair)

| Метрика | Пик (мгновенный) | Sustained (для контраста) | Источник |
|--------|-------------------|---------------------------|----------|
| **Download (Ookla)** | **~50 Mbps** (потом спад) | **18.28 Mbps** | Ты в чате 20.05, Result ID `19213862301`; `docs/BENCHMARKS.md`, `docs/THROUGHPUT_HYPOTHESES.md` |
| **Download (Ookla, фаза 0)** | **~60 Mbps** на графике | **~15 Mbps** | `bench-results/bench_2026-05-20_1547.txt`, `BENCHMARKS.md` — явно **host ↔ host** |
| **Upload (Ookla)** | **~53 Mbps** (fast.com) | — | Чат 19.05, fast.com |
| **Upload (Ookla)** | **~56 Mbps** (ты писал «7 МБ/с») | — | Чат 19.05, speedtest by Ookla до обрыва VPN |
| **curl 92 MB** | не speedtest; avg **~8 Mbps** | обрыв на 96% | Фаза 0, host/host |
| **curl (сессия 07:44)** | оценка **~12 Mbps** (~1.52 MB/s) | 84% файла | `app.log`, ICE **host** `192.168.1.103 ↔ VPS` |
| **YouTube 4K** | субъективно «норм» | — | Чаты 19–20.05 при стабильном host |

**Итог по HOST:** зафиксированный **максимальный пик ≈ 60 Mbps** (график Ookla, 20.05). Чаще в текстах фигурирует **~50 Mbps** на пике speedtest. **Sustained** по прод-замеру — **~18 Mbps DL / ~17 Mbps UL** (VP8 24×30, video mode).

---

## RELAY (ICE TURN-only, `ICETransportPolicy=relay`, KCP-over-VP8)

| Метрика | Пик (мгновенный) | Sustained | Источник |
|--------|-------------------|-----------|----------|
| **KCP-over-VP8 (bulk)** | **до ~2.3 Mbps** | **~1.2 Mbps** | `docs/TRANSPORT_AUDIT.md`, `docs/THROUGHPUT_PLAN.md` |
| **TURN (вывод плана)** | — | **~1–2 Mbps** (cap одного stream) | `docs/TRANSPORT_FIX_PLAN.md` |
| **VP8 без KCP на «лоссивом» TURN** | — | **~250 kbps** (худший кейс) | `TRANSPORT_FIX_PLAN.md` |
| **DC mode в relay** | — | **53 kbps** | Замер фазы 2, `TRANSPORT_FIX_PLAN.md` |
| **curl 92 MB, 100%** | — | порядка **1.2 Mbps** sustained при KCP | Успешный прогон после Hybrid-B |
| **Speedtest пик ~50 Mbps на strict relay** | **нет в документах** | — | Отдельного Ookla/host-style пика для **только relay** не записано |

**Итог по RELAY:** **пик ≈ 2.3 Mbps**, **sustained ≈ 1.2 Mbps** (KCP + TURN, формальные замеры). Это на порядок ниже host.

---

## Важные оговорки

1. **«50 Mbps» в гипотезах (B2)** относится к **host/speedtest-пикам**, не к relay. В `THROUGHPUT_PLAN` рядом с `kcp-rate` (пульсация 0.6–1.6 **Mbps**) — это **другой** эксперiment; не путать с пиком 50.

2. **Ранний прод 18.28 + пик ~50** в таблице помечен как `relay?` — ICE в логе того прогона не всегда явно relay; **надёжнее** считать пик **50–60 Mbps** отнесённым к **direct/host** (фаза 0 это подтверждает).

3. **Сейчас у тебя default = relay** — ожидай **не 50 Mbps**, а порядка **1–2.3 Mbps** на bulk, пока не вернёшь `VK_VPN_ICE_TRANSPORT_POLICY=all` или не сделаешь bonding.

4. Замеры **не «сырой UDP iperf»**, а **VP8/KCP внутри звонка VK** — пик на секунду графика speedtest или строки `kcp-rate … Mbps`.

---

## Одной строкой

| Режим | Макс. пик (из истории проекта) | Типичный sustained |
|-------|------------------------------|-------------------|
| **HOST** | **~60 Mbps** (график), чаще **~50 Mbps** | **~15–18 Mbps** DL |
| **RELAY** | **~2.3 Mbps** | **~1.2 Mbps** |

Чтобы **документировать** пик relay отдельно (как для host), нужен один прогон: Ookla + `kcp-rate` в логе при `ICE selected … relay/udp` — в чатах этого пика **нет**, есть только KCP-метрики ~2.3 Mbps.