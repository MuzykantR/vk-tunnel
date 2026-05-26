# План: максимальный throughput vk-vpn в `relay` режиме

**Status:** В работе. **Цель НЕ достигнута** (несмотря на промежуточный обнадёживающий замер).
**Predecessor:** `docs/TRANSPORT_FIX_PLAN.md` — план «100% доставка» (закрыт ✅).
**Current state:** см. `docs/TRANSPORT_AUDIT.md`.

> **История:** baseline 1.3 Mbps → попытка отключить pion WebRTC GCC (env `VK_VPN_DISABLE_GCC=1`) → один прогон curl показал 52 Mbps за 13.9 сек. Я поспешно объявил победу, потом повторные прогоны вернули 0.5-2 Mbps. **52 Mbps был burst, не sustained.** Реальная картина: GCC throttling действительно проблема, но мой fix («просто убрать RTCPFeedback из codec») неполный — нужно правильно сконфигурировать GCC interceptor с floor + Pacer + DegradationPreference.

---

## 0. TL;DR (актуально на 2026-05-26 поздно)

- **Baseline на default config:** ~1.3 Mbps sustained.
- **Лучший наблюдённый замер:** 52 Mbps за 13.9 сек curl — **burst**, не sustained.
- **Sustained при «GCC off» (мой первый fix):** ~300-500 kbps по серверным логам.
- **Корень проблемы (по Gemini analysis):** pion's WebRTC GCC + Pacer interceptors throttle'ят VP8 outbound rate под наблюдаемый «loss» от KCP retransmit bursts. Простое «убрать feedback из codec» — не отключает pacer и не задаёт floor для BWE.
- **Правильный фикс (T-GCC v2):** регистрировать GCC interceptor **явно** с hardcoded floor `MinBitrate=10 Mbps, MaxBitrate=100+ Mbps`, использовать LeakyBucketPacer с 20 Mbps baseline, и зашить `DegradationPreference: Disabled` на RTPSender. **Сохранить TWCC живым** (SFU heartbeat).
- **Цель:** sustained 5+ Mbps минимум, 10+ Mbps стрейч. **Не достигнута.**

---

## 1. Что мы знаем после фейк-победы

### Закрытые гипотезы (отрицательно или неактуально)

| Гипотеза | Статус | Причина |
|---|---|---|
| H1 — KCP env-sweep | ❌ Опровергнута | Defaults оптимальны, sweep не дал прироста |
| H4 — bonding | ❌ Закрыта отрицательно | RR + blocking write → stall на одном sub; per-session cap всё равно скорее всего есть |
| H12 — vkturnproxy-стиль | ❌ Отменена | У них тот же ~1 Mbps cap без WRAP-обфускации; у нас другая природа |
| H14 — QUIC-over-VP8 | ❌ Отменена | Anton48 (vkturnproxy) пробовал — то же что KCP |
| H13 v1 — пустой InterceptorRegistry | ❌ Не работает стабильно | Дал 52 Mbps burst в одном прогоне, потом sustained просел до 0.3-0.5 Mbps |

### Открытая основная гипотеза

| Гипотеза | Статус | Источник |
|---|---|---|
| **H13 v2 — GCC interceptor с hardcoded floor + Pacer + DegradationPreference** | 🟢 Главная | Gemini analysis 2026-05-26 |

Идея: НЕ удалять GCC interceptor (это ломает TWCC feedback → VK SFU считает клиента мёртвым → дропает сессию). Вместо этого:
1. Зарегистрировать GCC явно с параметрами:
   - `WithInitialStartBitrate(2 Mbps)` — для handshake/negotiation
   - `WithMinBitrate(10 Mbps)` — floor, ниже которого pacer не опустится
   - `WithMaxBitrate(150 Mbps)` — потолок
   - `WithSendSideBWE(true)` — sender-side BWE активен (для совместимости)
2. Использовать `LeakyBucketPacer` с 20 Mbps baseline serialization rate — равномерно выпускать пакеты, не создавать burst flood'ов которые ломают handshake.
3. На каждом `RTPSender.GetParameters() → DegradationPreference=Disabled → SetParameters()` — pion не может «деградировать» rate динамически.
4. **Сохранить TWCC feedback в SDP** — SFU нужен heartbeat.

---

## 2. Поэтапный план

### Phase T-GCC v2 — Правильный GCC tuning (текущий приоритет)

Файлы:
- `vk-vpn-client/tunnel/codec_setup.go` — helper для сборки MediaEngine + Interceptor Registry с правильной конфигурацией
- `vk-vpn-server/creator/session.go` — использовать helper, добавить DegradationPreference на VP8 sender
- `vk-vpn-client/webrtc/joiner.go` — то же на клиенте
- `go.mod` — может потребовать `github.com/pion/interceptor/pkg/gcc` и `.../pacer`

### Phase T-FLAP — Robustness recovery (после T-GCC)

При WS reset / call recycle / ICE flap сессия зависает. Это блокирует **долгие** замеры. Чинить SDP glare resolution в creator/p2p.go.

### Phase T-FINAL — Замеры и фиксация

После T-GCC v2:
- Замер sustained на 92 MB curl × 3 подряд
- Замер UL через Speedtest
- Зашить env-defaults в systemd unit + Go defaults
- THROUGHPUT_REPORT.md + AUDIT.md update

---

## 3. Acceptance criteria

- **Минимум:** sustained ≥ 5 Mbps в curl 92 MB **× 3 подряд** (не один short burst).
- **Стрейч:** sustained ≥ 10 Mbps.
- **Не ломать:** 100% delivery; ICE handshake стабильный; SDP negotiation проходит.

---

## 4. История замеров

| Дата | Конфиг | curl avg | Тип | Комментарий |
|---|---|---|---|---|
| 2026-05-24 | bonding=1, GCC=on, defaults | 1.31 Mbps | sustained 10 мин | Hybrid-B первый замер |
| 2026-05-25 | bonding=1, GCC=on, KCP-sweep | 1.18 Mbps avg | sustained × 4 | T1: env-tuning исчерпан |
| 2026-05-25 | bonding=2, GCC=on | 0 Mbps (stall) | failed | T3: bonding broken (RR+blocking) |
| 2026-05-26 12:00 | bonding=1, GCC=off (server only) | **52 Mbps** | **burst 13.9 сек** | Фейк-победа: один короткий замер |
| 2026-05-26 14:30 | bonding=1, GCC=off (sym) | 0.5-1.0 Mbps | sustained | Возврат к baseline или хуже |
| 2026-05-26 14:35 | bonding=1, GCC=off | 7 Mbps | burst 5 сек | Снова короткий burst |
| 2026-05-26 14:40 | bonding=1, GCC=off | 0.68 Mbps | sustained | Стабильно низкая |

**Вывод по замерам:** наблюдаемая «производительность» очень нестабильная (от 0.5 до 50 Mbps в зависимости от состояния канала и момента). Стабильный sustained на конфиге «GCC off (пустой interceptor registry)» — **хуже baseline**. Нужен **полный пакет** настроек GCC по Gemini.

---

## 5. Что НЕ гарантировано

1. **Sustained 5+ Mbps достижим**. Gemini рекомендация может тоже не сработать — pacer/BWE interactions с KCP burst-pattern'ом сложные. Может потребоваться custom NoOpPacer.
2. **VK SFU отреагирует на high sustained rate**. Если мы выйдем на 20-30 Mbps стабильно, VK видит аномалию для «видеозвонка» и может начать шейпить наш паттерн.
3. **DPI VK не следит за паттерном VP8 inside WebRTC**. У них с vkturnproxy DPI следит за паттерном TURN ChannelData, у нас другая природа трафика, но риск шейпинга **есть**.

---

## 6. Резюме одной строкой

Cap ~1.3 Mbps **является артефактом WebRTC GCC**, но **простого "отключить" недостаточно** — нужна правильная конфигурация interceptor chain с hardcoded floor + дисциплинированный pacer + locked sender parameters. Реализация по Gemini analysis — в работе.
