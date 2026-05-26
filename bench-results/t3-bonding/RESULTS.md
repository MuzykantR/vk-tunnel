# T3 KCP Bonding — Results

**Дата:** 2026-05-26.
**Ветка кода:** `feat/bonding-dual-kcp` (210d730).
**Baseline:** `bench-results/baseline_2026-05-25.md` — sustained 1.31 Mbps на defaults.

## Сводная таблица

| Прогон | bonding | curl avg KB/s | Mbps | Завершился | Дельта vs baseline |
|---|---:|---:|---:|---|---:|
| C | 1 | 147.8 | 1.18 | ✅ 100% за 10:36 | −10% (шум канала) |
| D | 2 | 0 | 0 | ⛔ user-stop ~24s | **−100%** |

## Прогон C — bonding=1 (контроль)

Sustained 1000-1700 kbps в kcp-rate. Поведение **идентично pre-bonding коду**. Просадка от вчерашнего baseline 1.31 → сегодня 1.18 — это **±15% дневной разброс** канала VK TURN, не регрессия.

**Acceptance "backward compat at N=1": ✅ выполнено.**

## Прогон D — bonding=2 (главный замер)

Связь установилась корректно: 2 sub-tracks, `BONDED (2 tracks)` на обеих сторонах, обе пары track-paired (`bond-idx=0` / `bond-idx=1`). Все checks "SDP negotiation OK" пройдены.

**Но поток данных не идёт:**

```
13:35:10  sub A: in=252  out=2342 (+2103, 1.7 kbps)
13:35:10  sub B: in=78   out=137  (+71)
...
13:36:20  sub A: in=265  out=2695
13:36:20  sub B: in=78   out=476
```

За весь 100-секундный прогон cumulative `in` (полезные данные от сервера) — **всего 343 байта суммарно через оба sub**. Это **handshakes + 200-байтный HTTP response header**, и больше **ничего**.

Skew между sub-tunnels:
- Sub A нагружен в **5× раз сильнее** sub B по out (`2695 vs 476`).
- На сервере та же картина с самого начала (`out=239 vs out=52` за первые 10 сек).
- Round-robin **должен** давать 50/50.

## Диагноз — stall из-за blocking RR + back-pressure

`KCPTunnel.SendData` блокирует на `session.Write` пока KCP send-window не освободит место. При неравных скоростях sub-tunnel'ей это даёт **deadlock-подобный паттерн**:

1. VK TURN режет один из видеотреков жёстче (асимметрично — гипотеза).
2. Send window забитого sub'а заполняется.
3. `BondedTunnel.SendData → b.subs[idx].SendData(wrapped)` блокирует **на той же горутине**, что и `RelayBridge.connectTCP read-loop`.
4. Read-loop не успевает читать от origin → origin closes TCP → `rx=0`.
5. Reorder buffer на receiver ждёт пропущенный seqno из забитого sub → emit замораживается.

**Это не per-session cap у VK TURN** — это **bug нашего dispatcher'а**. Цифру cap на bonded мы не получили.

## Решение

**Bonding в текущей форме откатываем.** Чинить требует:
- non-blocking dispatch (за-buffer'ить frames на нашей стороне, не давать одному sub блокировать другие);
- weighted RR с per-sub RTT-aware weights;
- timeout-based emit в reorder buffer на случай stuck sub.

Это ещё ~500 LoC. **В лучшем случае** даёт baseline (если per-session cap у VK реально есть), в худшем — те же 0 bytes при экстремальном skew. Плохая инвестиция перед другими гипотезами.

**Переходим к H13 (отключение WebRTC GCC).** Это `~50 LoC`, потенциал **+30-100% на single stream**. Если single stream вытянет до 2-3 Mbps на bonding=1 — это уже даёт нам полезный продукт.

Bonding можно вернуть позже как **T6** с правильным dispatcher'ом, **только если** H13 покажет, что VK TURN не имеет жёсткого per-session cap.

## Артефакты

- `run-C-bonding1.log` — контрольный прогон, 941 строка
- `run-D-bonding2.log` — bonding=2 stall, 190 строк
