# План: максимальный throughput vk-vpn в `relay` режиме

**Status:** утверждён 2026-05-25.
**Predecessor:** `docs/TRANSPORT_FIX_PLAN.md` — план «100% доставка» (закрыт ✅).
**Current state:** см. `docs/TRANSPORT_AUDIT.md`.
**Related:** `vkturnproxy.md` (альтернативный проект, источник цифры «5 Mbps на 1 поток»), `docs/relayhost.md` (свод исторических замеров).

> Цель плана 100% доставки достигнута. Теперь нужна **скорость**. Sustained 1.2 Mbps не годится для нормального серфинга. Этот план — про **выжимание максимума** из архитектуры Hybrid-B (KCP-over-VP8 + relay), с честными ограничениями.

---

## 0. TL;DR

- **Текущее:** sustained ~1.2 Mbps, burst до 2.3, 100% delivery.
- **Эмпирический потолок 1 потока через VK TURN ≈ 5 Mbps** (из README проекта [vk-turn-proxy](https://github.com/cacggghp/vk-turn-proxy): *«-n 1 для более стабильного подключения в 1 поток (ограничение 5 МБит/с для ВК)»*). Принимаем как **рабочее допущение**, а не догму.
- **Цель ближняя:** sustained ≥ 5 Mbps (выйти на потолок одиночного потока).
- **Цель стрейч:** sustained ≥ 10 Mbps (потребует multi-stream bonding'а).
- **Альтернатива на горизонте:** переход на архитектуру vkturnproxy-стиля (TURN ChannelData без WebRTC) — потенциал до 25 Mbps, но переписка ~70% проекта.

---

## 1. Что мы знаем и чего не знаем

### Знаем (зафиксированные факты)

| Факт | Источник |
|---|---|
| Sustained KCP-over-VP8 в relay = 1.2 Mbps | TRANSPORT_AUDIT.md §4 |
| Burst до 2.3 Mbps | то же |
| 100% delivery при 2-12% physical loss VP8 | то же |
| SCTP DC в relay = 53 kbps (отвергнут) | TRANSPORT_FIX_PLAN.md Фаза 2 |
| Raw VP8 без ARQ теряет 5-15% байт | то же |
| 1 поток через VK TURN ≈ 5 Mbps cap (другой проект) | vkturnproxy.md |
| vkturnproxy достигает 25+ Mbps multi-stream | то же |

### Не знаем (требует проверки)

| Вопрос | Кто блокирует решение | Как проверить |
|---|---|---|
| VK TURN капит **per-track** или **per-session**? | Решение про bonding (T3) | Probe H11: искусственно поднять keepalive до 200 pps в нашем коде, замерить, просел ли curl-goodput. ~5 строк. |
| Реальный overhead FEC 10:3 vs FEC=off на нашем канале | Решение про defaults FEC | A/B curl |
| Влияет ли MTU интерфейса tun (1400 сейчас) на goodput | Опциональный твик | A/B curl с MTU 1500 (как у whitelist-bypass). НУЖНА ПРОВЕРКА. |
| KCP MTU 1200 vs 1100 — есть ли IP-фрагментация в TURN-пути | Опциональный твик | A/B curl |
| Зарубежный VPS-провайдер с лучшим peering до VK TURN существует? | Решение про T5 | Только если код-оптимизации упёрлись. Дорогая операционная задача. |

---

## 2. Гипотезы (ранжированы по приоритету)

Приоритет = **(потенциальный прирост) × (вероятность сработать)** / **(стоимость реализации)**.

### Приоритет 1 — H1: KCP env-sweep (дёшево, средний потенциал)

Текущие defaults `FEC=10:3, NODELAY=1,10,2,1, WINDOW=2048, MTU=1200` выбраны интуитивно. На RTT 60-90 ms + 2-12% loss оптимум может быть другим.

| Параметр | Default | A/B значения | Ожидание |
|----------|---------|--------------|----------|
| `VK_VPN_KCP_FEC` | `10:3` | `off`, `10:1` | +18-30% полезной полосы за счёт уменьшения parity overhead, если потери не съедят retransmit |
| `VK_VPN_KCP_NODELAY` | `1,10,2,1` | `1,5,2,1` | Чаще тики → быстрее реакция на ACK → выше sustained |
| `VK_VPN_KCP_WINDOW` | `2048` | `4096` | Поможет, если упираемся в window (видно по `out` не растёт при свободной полосе) |
| `VK_VPN_KCP_MTU` | `1200` | `1100` | Меньше IP-фрагментации в TURN UDP. НУЖНА ПРОВЕРКА. |

**Ожидаемый эффект:** sustained 1.5-2.5 Mbps (+25-100%).
**Стоимость:** 0 кода (всё через env). ~3 часа на прогоны.
**Риск:** низкий — откат без пересборки.

### Приоритет 2 — H11: проба per-track vs per-session cap

**Это решающая проба перед любым bonding-кодом.** ~5 строк правки.

Идея: искусственно поднять KCP keepalive с 10 pps до 200 pps. Замерить, что произойдёт с curl-goodput.

- Если goodput **просел на сопоставимую величину** (~200 kbps) → cap по **сессии в целом** → bonding бесполезен → план Б идёт через H10/H12.
- Если goodput **не изменился** → cap по типу/треку → bonding имеет шанс → идём в T3.

**Стоимость:** ~5 строк + 1 час замеров.
**Без этой пробы H4 (bonding) — лотерея.**

### Приоритет 3 — H4: Bonding dual/triple KCP над несколькими VP8 tracks

**Делается только если H11 показал per-track cap.**

Архитектура:
```
RelayBridge.SendData(frame) → load_balance(kcp1, kcp2, ...) → frame идёт через один из tunnels
                                                            ← merge_by_seqno на приёме
```

Изменения:
- Frame-level sequence number в protocol.
- Reorder buffer в RelayBridge.
- Второй (третий) `TrackLocalStaticSample` в `joiner.go` / `session.go`.
- Второй (третий) `KCPTunnel` поверх своего track'а.
- Round-robin или weighted dispersion frames.

**Ожидаемый эффект:** sustained 3-6 Mbps (×2 bonding), до 5-10 Mbps (×3-4 bonding) при удаче.
**Стоимость:** ~1000 LoC, 3-5 дней. Объёмная работа.
**Риск:** средний. **Может ничего не дать** если H11 покажет per-session cap (поэтому H11 первый).

### Приоритет 4 — H7: keepalive gating

Не слать VP8 keepalive, если за последние 200 ms был исходящий data-пакет. Освобождает ~10 pps из бюджета канала под полезные данные.

**Ожидаемый эффект:** +5-10%.
**Стоимость:** ~10 строк.
**Риск:** очень низкий (keepalive нужен только в idle).

### Приоритет 5 — H2: лимит параллельных CONNECT (UX, не throughput)

На малой полосе важнее **меньше параллельных flows**, чем «выжать больше». Браузер открывает 30+ HTTP — каждый получает 1/30 от полосы. Если capped по 8-16 — каждый flow получает больше, страница грузится **субъективно** быстрее, хотя итоговый Mbps тот же.

`VK_VPN_RELAY_CONNECT_LIMIT=N` уже существует. Подобрать N.

**Ожидаемый эффект:** subjective UX. Throughput не меняется.
**Стоимость:** 0 кода, ~30 мин подбора.
**Делать отдельным треком**, не блокирует основную линию.

### Приоритет 6 — H8: MTU tun-интерфейса (1400 → 1500)

Whitelist-bypass использует MTU 1500. У нас сейчас 1400. Гипотеза: бо́льший MTU → меньше TCP-сегментов на тот же объём → меньше pps в пути.

**Ожидаемый эффект:** +3-8% при крупных transfer'ах, может ничего на коротких HTTP.
**Стоимость:** правка `vk-client/wintun/` + smoke. НУЖНА ПРОВЕРКА: что wintun корректно пропустит 1500 при KCP MTU 1200 — возможна сегментация ОС → больше KCP-фреймов вместо меньше.
**Риск:** низкий, легко откатить.

### Приоритет 7 — H5: новый зарубежный VPS

Не «ближе к VK TURN» (мы VPN-сервис, нужны зарубежные VPS), а «зарубежный провайдер с лучшим peering до AS VK TURN».

**Делается только** если код-оптимизации упёрлись и cap = network-side, а не наш стек.

**Стоимость:** $$ + 1-2 дня на деплой.
**Риск:** может не дать ничего, если cap действительно на TURN policer (а не в peering).

### Backstop — H12: альтернативная архитектура vkturnproxy-стиля

Полная переписка транспорта: вместо WebRTC PeerConnection использовать **TURN ChannelData напрямую** + DTLS обфускация + параллельные потоки. Поверх — WireGuard или сохранить наш SOCKS5 (или VLESS+KCP+smux как у них).

**Ожидаемый эффект:** до 25 Mbps.
**Стоимость:** 3-4 недели, переписка ~70% проекта.
**Риск:** VK может policer'ить «не-звонковый» TURN-трафик иначе. У vkturnproxy этого риска нет (по их опыту), но они принимают этот риск явно.

**Решение по H12 откладываем** до завершения T1-T4. Если 5 Mbps достигнут — оставляем как backlog. Если упёрлись в 2-3 Mbps и bonding бесполезен — открываем H12.

### Отброшенные гипотезы

- **TURN retry (старый H3)** — отброшено. Все VK TURN'ы под одним policer'ом.
- **Pure UDP baseline через TURN** — отброшено. Самопальный probe не похож на VP8-трафик, замер не интерпретируем. Эмпирический cap взят из vkturnproxy README.
- **Multi-call bonding (несколько звонков параллельно)** — отброшено. Высокий VK-risk.

---

## 3. Поэтапный план — порядок работ

### Фаза T0 — Baseline (1 прогон, ~30 мин пользователя)

| # | Действие | Кто | Результат |
|---|----------|-----|-----------|
| T0.1 | Собрать актуальный клиент (`wails build`), убедиться, что сервер на актуальном main с `VK_VPN_TUNNEL_MODE=kcp` | пользователь | exe готов |
| T0.2 | curl 92 MB × **1 раз** (15-мин прогон уже даёт усреднение, повторы избыточны), записать avg Mbps | пользователь | строка из curl |
| T0.3 | Speedtest × **1 раз** через VPN, записать DL/UL/ping | пользователь | пара цифр |
| T0.4 | Прислать: строку curl, цифры Speedtest, последние ~200 строк `app.log` + ~200 строк `/var/log/vk-vpn-daemon.log` | пользователь | артефакты |
| T0.5 | Заполнить `bench-results/baseline_2026-05-25.md` | агент | baseline зафиксирован |

**Acceptance:** есть зафиксированная цифра sustained ± burst для текущей default-конфигурации.

**Если первый прогон дал странный результат** (обрыв, sustained < 0.5 Mbps или > 3 Mbps) — повторить, разбираться.

### Фаза T1 — KCP env-sweep (H1)

| # | A/B | Что меняем (env) | Замер |
|---|-----|-------------------|-------|
| T1.1 | FEC=off | `$env:VK_VPN_KCP_FEC = "off"` | curl 92 MB × 1 |
| T1.2 | FEC=10:1 | `$env:VK_VPN_KCP_FEC = "10:1"` | curl 92 MB × 1 |
| T1.3 | NODELAY agressive | `$env:VK_VPN_KCP_NODELAY = "1,5,2,1"` | curl 92 MB × 1 |
| T1.4 | WINDOW увеличить | `$env:VK_VPN_KCP_WINDOW = "4096"` | curl 92 MB × 1 |
| T1.5 | KCP MTU вниз | `$env:VK_VPN_KCP_MTU = "1100"` | curl 92 MB × 1 |
| T1.6 | Лучшая комбинация | объединить выигравшие из T1.1-T1.5 | curl 92 MB × **2** (один на подтверждение разброса) |
| T1.7 | Зашить best в `kcp_env.go` defaults, коммит | агент | PR |

**Acceptance:** sustained ≥ 1.5× baseline (т.е. ≥ 1.8 Mbps), **или** обоснованный вывод «defaults уже близки к оптимуму, +10-20% максимум».

**Decision gate после T1:** если sustained ≥ 3.5 Mbps → мы близко к single-stream cap (~5), переходим к H7+H2 (микро-оптимизации) и **сразу к T3 (bonding)**. Если 1.5-3 Mbps → есть запас, идём в T2 (per-track probe).

### Фаза T2 — Probe per-track cap (H11)

**Условие:** T1 не вышел на ≥ 3.5 Mbps.

| # | Действие | Кто | Результат |
|---|----------|-----|-----------|
| T2.1 | Ветка `chore/turn-cap-probe`: поднять keepalive в `kcp_packetconn.go` с 10 pps до 200 pps (~5 строк) | агент | PR (НЕ в main) |
| T2.2 | Сборка, curl 92 MB × 1 с этой веткой | пользователь | sustained + лог `kcp-rate` |
| T2.3 | Анализ: просел ли goodput на ~200 kbps относительно T0/T1-best? | агент | вердикт |

**Acceptance:** один из двух результатов:
- **per-session cap** → bonding бесполезен → переходим к T4 (VPS) или сразу к T5 (H12).
- **per-track cap** → bonding имеет смысл → переходим к T3.

### Фаза T3 — Bonding (H4)

**Условие:** T2 показал per-track cap.

| # | Задача | Файл | Сложность |
|---|--------|------|-----------|
| T3.1 | Frame-level seqno в protocol (новое поле или новый MsgType) | `tunnel/protocol.go` | низкая |
| T3.2 | Reorder buffer в RelayBridge на receiver | `tunnel/relay_bridge.go` | средняя |
| T3.3 | Второй `TrackLocalStaticSample` в session/joiner | `creator/session.go`, `webrtc/joiner.go` | средняя |
| T3.4 | Второй `KCPTunnel` поверх второго track'а | те же файлы | средняя |
| T3.5 | Dispatcher: round-robin или weighted | `tunnel/bonding.go` (новый) | высокая |
| T3.6 | Sanity-замеры bonding on/off | scripts | — |

**Acceptance:** sustained ≥ 1.7× single-stream best. Если ≤ 1.2× — bonding не дал; разбираемся, возможен skew по очередям.

**Время:** 3-5 дней.

### Фаза T4 — Зарубежный VPS (H5) — опционально

**Делается только** если T1-T3 упёрлись в потолок, а замеры показывают, что cap network-side.

- mtr/traceroute с кандидатов до IP TURN, выданных VK в signaling.
- Сравнить RTT, hop count, отсутствие сильных детуров через РФ.
- Деплой на тот, у которого RTT 30-50 ms (vs текущих 60-90).

### Фаза T5 — Backstop H12 — отдельный долгосрочный план

**Не часть этого плана.** Если T1-T4 не достигли минимум-цели в 5 Mbps, открываем отдельный документ `ARCHITECTURE_V2_TURN_DIRECT.md` и считаем переписку.

### Фаза T6 — Финальный отчёт

После достижения acceptance плана в целом:
- `docs/THROUGHPUT_REPORT.md` с фактическими цифрами.
- Update `TRANSPORT_AUDIT.md` с новыми Mbps.
- Лучшие defaults зашиты в код.

---

## 4. Acceptance criteria для плана в целом

- **Минимум:** sustained ≥ 5 Mbps в curl 92 MB.
- **Стрейч:** sustained ≥ 10 Mbps, видеозвонок 720p проходит.
- **Не ломать:** 100% delivery должна сохраняться (regression test).

---

## 5. Что НЕ гарантировано

1. **5 Mbps достижимы**. Цифра «5 Mbps на 1 поток» — из чужого проекта на другой архитектуре (TURN ChannelData, не VP8). У нас VP8-обёртка может иметь свой потолок ниже. Вероятность достижения 5 Mbps — оцениваю в 60-70%.
2. **Bonding × N даёт ×N**. Зависит от поведения VK TURN policer'а. Бинарный исход, проверяется в T2.
3. **Любая оптимизация даст линейный прирост**. KCP-CC и FEC взаимосвязаны: меньше FEC → больше retransmit на потерях → throughput может **упасть**.

---

## 6. Структура будущих коммитов

```
HEAD (текущий main: docs THROUGHPUT_PLAN closed)
│
├─ docs(throughput): rewrite plan — drop udp-probe, raise bonding, anchor on vkturnproxy 5Mbps cap
├─ bench(baseline): record 2026-05-25 numbers on default config
├─ feat(kcp): tune defaults — <лучший вариант из T1>
├─ chore(probe): keepalive 200pps probe for per-track-cap test  (не в main, отдельная ветка)
├─ feat(transport): dual-VP8 bonding with frame-level seqno     ← если T2 положительный
├─ docs: THROUGHPUT_REPORT.md
└─ chore: update AUDIT.md with throughput numbers
```

---

## 7. Ожидаемые цифры по сценариям (на основе vkturnproxy cap)

| Сценарий | Sustained Mbps | Примечание |
|----------|----------------|------------|
| Status quo (baseline) | 1.2 | факт |
| После KCP env-sweep | 1.5-2.5 | если FEC оверкилл |
| + keepalive gating | +5-10% | независимый прирост |
| Single-stream потолок | ~3-4 | наш overhead под TURN cap 5 Mbps |
| Bonding × 2 (если per-track) | 3-6 | удваивает ёмкость |
| Bonding × 3-4 (если per-track) | 5-10 | стрейч-цель достижима |
| H12 vkturnproxy-стиль | 10-25 | переписка, отдельный sprint |

---

## 8. Резюме

Цель `TRANSPORT_FIX_PLAN.md` (100% доставка) достигнута. Sustained 1.2 Mbps — про reliability over speed. Чтобы вылезти за 5 Mbps:

1. **Сначала T1 (KCP env-sweep)** — дёшево, может дать ×2.
2. **Потом T2 (per-track probe)** — 5 строк, разрешит ключевую неизвестную.
3. **Если per-track** → T3 (bonding), 1000 LoC, потенциал ×2-4.
4. **Если per-session** → T4 (новый VPS) или T5 (vkturnproxy-стиль архитектура).

Эмпирический cap одного потока через VK TURN ≈ 5 Mbps (vkturnproxy). Единственный путь существенно выше — multi-stream bonding **или** смена архитектуры на TURN ChannelData напрямую.
