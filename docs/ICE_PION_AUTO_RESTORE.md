# Pion ICE: автовыбор vs TURN-only (заглушка для агентов)

> **Не придумывать новую логику** — она уже была в репозитории. Этот файл фиксирует коммит и два режима.

## Последний коммит с автоматическим выбором pion

| Поле | Значение |
|------|----------|
| **Коммит** | `48d4780` — `feat: add relay benchmark documentation and enhance ICE wait time configuration` |
| **Также** | `18b6c21`, `8ca9bfa`, `05eaabc` — то же по сути (без `ICETransportPolicy`) |
| **Файл** | `vk-vpn-client/webrtc/ice_settings.go` |
| **Joiner** | `NewPeerConnection(webrtc.Configuration{ICEServers: ice})` — **без** поля `ICETransportPolicy` (= pion default `all`) |

Проверка:

```bash
git show 48d4780:vk-vpn-client/webrtc/ice_settings.go
git show 48d4780:vk-vpn-client/webrtc/joiner.go | findstr NewPeerConnection
```

### Как работал «pion auto» (не путать с RelayBridge)

1. **`ICETransportPolicy` не задаётся** → pion номинирует host / srflx / prflx / **relay** по connectivity checks.
2. **`SetRelayAcceptanceMinWait(8s)`** (env `VK_VPN_ICE_RELAY_WAIT`) — pion **откладывает** выбор TURN, чтобы успеть direct (WLB-style).
3. Лог: `ICE direct path` или `ICE uses TURN relay` в `icepair.go` — только наблюдение, не принуждение.

`relay: close` в serverlog — это **TCP relay приложения** (SOCKS→VPS), не ICE TURN.

---

## Текущий тестовый режим (рабочая копия, может быть uncommitted)

В `ice_settings.go` → `ICETransportPolicyFromEnv()`:

- **default** → `ICETransportPolicyRelay` (TURN-only для тестов whitelist).
- `VK_VPN_ICE_TRANSPORT_POLICY=all|auto|pion` → вернуть pion auto **без правки кода**.

См. план Cursor: `phase-prod-ice-auto` в `throughput_hypotheses_implementation_*.plan.md`.

---

## Восстановление релиза (буквально «поменять default»)

**Вариант A — env (уже работает):** на клиенте и VPS:

```text
VK_VPN_ICE_TRANSPORT_POLICY=all
```

**Вариант B — сменить default в коде** (`ice_settings.go`, функция `ICETransportPolicyFromEnv`):

```go
// default: return pion.ICETransportPolicyAll   // prod
// case "relay": return pion.ICETransportPolicyRelay  // тест
```

И в `ApplyICEPerformanceSettings`: при `policy == All` снова вызывать `SetRelayAcceptanceMinWait` (как в `48d4780`).

**Не нужно:** отдельный ICE state machine, принудительный restart для выбора relay, дублирование WLB redirect — см. `vk-client/main.go` + `icepair.go`.

---

## Связанные env (из `48d4780`)

| Env | Default | Назначение |
|-----|---------|------------|
| `VK_VPN_ICE_RELAY_WAIT` | 8s | Только при `policy=all` |
| `VK_VPN_ICE_PREFER_DIRECT_WAIT` | 3s | Redirect: ждать direct pair stats |
| `VK_VPN_ICE_TRANSPORT_POLICY` | *(тест: relay)* | `all` = pion auto |

Док бенчмарка: [RELAY_BENCHMARK.md](./RELAY_BENCHMARK.md).
