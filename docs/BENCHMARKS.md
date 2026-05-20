# Бенчмарки vk-vpn

Протокол: [BENCHMARK_PROTOCOL.md](BENCHMARK_PROTOCOL.md).  
Заметки прогона: [bench-results/bench_2026-05-20_1547.txt](../bench-results/bench_2026-05-20_1547.txt).

---

## Фаза 0 — итог (20.05.2026) ✅

**Сеть:** домашний Wi‑Fi (Россия). Speedtest: **без VPN** — [Яндекс Интернетометр](https://yandex.ru/internet/); **с VPN** — Ookla (speedtest.net), пик на графике ~60 Mbps.

| Прогон | DL | UL | Ping | ICE / примечания |
|--------|-----|-----|------|------------------|
| **Без VPN** | **72.45** Mbps | **70.82** Mbps | 14 ms | — |
| **С VPN** | **~15** sustained (пик **~60**) | **40** Mbps | 105 ms | **host ↔ host** (не relay); ICE stable, без restart на speedtest |
| **curl DL** (mirror.yandex.ru, ~92 MB) | **~8.0** Mbps avg | — | — | **Обрыв на 96%**: `curl: (18)`, не хватает **3 458 640** B; зависание на последних ~3.3 MB |

### Вывод фазы 0

1. **Линия не узкое место** (72/70 без VPN) → упираемся в **туннель VK/VP8**, не в домашний uplink ~20 Mbps.
2. **Sustained DL ~15 Mbps** при пике ~60 — похоже на burst SFU / буферы, не стабильный канал.
3. **ICE host/host** — хороший путь (без TURN relay); RTT 105 ms всё равно высокий (география клиент ↔ VPS).
4. **Хвост большого curl** — отдельная проблема стабильности: в логах сервера VP8 уходит в keepalive **24 B**, волна `[dc] close`, затем почти нет крупных `vp8: sent` → фаза 1 + разбор relay TCP на VPS.

### Логи при обрыве curl (фрагмент)

- `vp8: sent` до **26646 B**, затем в основном **1278–7318 B**, в конце **24 B** (keepalive).
- `[video] recv` в основном **24 B** (клиент почти не шлёт данные в uplink).
- `[dc] close` с `tx=0`, `rx` до **70121** — параллельные соединения (браузер/speedtest), не основной VP8-поток.
- После **13:20:34** — провал активности VP8 до следующего keepalive recv.

**Решение:** переход к **фазе 1** (throughput + backpressure). Вуз из бенчмарка **исключён** по решению команды.

---

## Прод — 20 мая 2026 (ранний baseline)

**Условия:** VP8 24×30, сервер `turgenev.ptr.network`.

| Метрика | Значение |
|---------|----------|
| Download | **18.28** Mbps |
| Upload | **17.37** Mbps |
| Ping | **107** ms |
| Пик | **~50** Mbps → спад |

Speedtest ID: 19213862301. 4K без ICE-flap.

См. также [THROUGHPUT_HYPOTHESES.md](THROUGHPUT_HYPOTHESES.md) §9.
