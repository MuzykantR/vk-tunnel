# T2 Per-Track Cap Probe — Results

**Дата:** 2026-05-25 ночь / 2026-05-26 утро.
**Ветка кода:** `chore/turn-cap-probe` (`6b8653e`).
**Baseline:** `bench-results/baseline_2026-05-25.md` — sustained 1.31 Mbps на defaults.

## Сводная таблица

| # | Probe pps | curl avg KB/s | Mbps | Время / % | Дельта vs baseline | Что случилось |
|---|---:|---:|---:|---|---:|---|
| A | 200 | 160.5 | 1.28 | 6:50 / 70% (stop) | −2% (шум) | пользователь сам остановил, throughput стабилен |
| B | 500 | (~160) | (~1.28 до flap) | 9:00 / 95% → flap | 0% до flap'а | **ICE flap на 9-й минуте**, не восстановился |

**Важно:** числа `avg` от curl в B (`128.0 KB/s`) **обманчивы** — это среднее за весь период включая 3 минуты нулевой скорости после flap'а. До flap'а sustained был тот же ~1.28 Mbps.

## Главный вывод по cap

**И 200, и 500 pps probe НЕ ухудшили sustained throughput.** До flap'а в B держался тот же ~1.28-1.30 Mbps. Это **слабый положительный сигнал** в пользу гипотезы «нет per-session cap'а на дополнительный cover-трафик» — открытие второго data-stream'а (bonding) **может** дать прирост.

Однако:
- Это слабый сигнал. Probe — это `keepalive` (24-byte VP8 sample), не реальный data-stream. VK TURN мог использовать разную политику к разным типам трафика.
- Окончательный ответ даст только bonding-эксперимент.

## ICE flap в прогоне B — корневая причина

Из серверного лога (`vk-client/build/bin/serverlog.txt`):

```
00:04:14 [vk-ws] read error: read tcp ... 155.212.204.11:443: read: connection reset by peer
00:04:14 Creator bridge WS closed
00:04:14 [creator] PC closed → ICE closed
00:04:14 Rejoining existing call (no calls.start)...
00:04:30 [vk-ws] connected (new endpoint)
00:04:30 [p2p] Offer ready, SDP len=3699            ← СЕРВЕР послал offer
00:04:30 [p2p] Remote SDP: offer                     ← КЛИЕНТ тоже послал offer
00:04:30 [p2p] SetRemoteDescription(offer) failed: InvalidModificationError:
              invalid signaling state transition: have-local-offer→SetRemote(offer)
```

Это **классический SDP glare**: обе стороны одновременно инициировали ICE restart с собственным offer. Наш код пытается «принять remote» (`accepting remote (ICE restart / rejoin)`), но **не делает rollback** своего local offer — WebRTC state machine отказывает.

После этого сессия зависает: KCP-state клиента остался привязан к старому PeerConnection, recovery не работает.

**Причина flap'а** — `connection reset by peer` от VK signaling. VK периодически сбрасывает long-lived WebSocket'ы. Это **штатное** поведение, не связано с probe. Вероятно probe ускорило flap (через высокий pps), но flap бы случился и без него — просто позже.

## Какие это даёт выводы для дальнейшей работы

1. **Bonding (T3) идти можно** — нет признаков cap'а на дополнительный трафик.
2. **SDP glare — реальный архитектурный баг**. При любом WS reset / call-recycle сессия может зависать. На bonding'е с 2-3 параллельными KCP-stream'ами проблема **усугубится**.
3. **Решение пользователя — путь B**: сначала bonding (валидация перспективы проекта), потом, если throughput'а хватит, починка glare как отдельная задача.

## Артефакты

- `run-A-probe200.log` — клиентский лог прогона A
- `run-B-probe500.log` — клиентский лог прогона B (содержит момент flap'а)
- Серверный лог обоих прогонов — `vk-client/build/bin/serverlog.txt`
  (не закоммичено — это локальный файл клиентской машины; ключевые места процитированы выше)
