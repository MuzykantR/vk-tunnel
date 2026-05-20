# Протокол бенчмарков vk-vpn (фаза 0)

Пошаговые замеры **до** изменений кода throughput. Результаты: [BENCHMARKS.md](BENCHMARKS.md), [bench-results/](../bench-results/), §9 в [THROUGHPUT_HYPOTHESES.md](THROUGHPUT_HYPOTHESES.md).

Шаблон: [scripts/bench-notes.ps1](../scripts/bench-notes.ps1).

---

## Скорость: какой сайт (фактическая схема замеров)

| Режим | Сервис | Зачем так |
|-------|--------|-----------|
| **Без VPN** | [Яндекс Интернетометр](https://yandex.ru/internet/) | В РФ без туннеля; Ookla часто недоступен |
| **С VPN** | [speedtest.net](https://www.speedtest.net) (Ookla) | Через туннель Ookla открывается; Яндекс с VPN может не отражать туннель корректно |

В заметках указывайте **страну** и **какой сайт** на каждый прогон (не путать без VPN ↔ с VPN).

---

## 0. Подготовка

1. Текущий деплой клиента + сервера (без экспериментальных env).
2. Env baseline:

```text
VK_VPN_TUNNEL_MODE=video
VK_VPN_VP8_FPS=24
VK_VPN_VP8_BATCH=30
VK_VPN_LOG_LEVEL=info
```

3. Логи клиента (joiner) + сервера (creator), строка **`ICE selected`**.

---

## 1. Линия без VPN (гипотеза A2)

| Шаг | Действие |
|-----|----------|
| 1.1 | VPN **выключен**. |
| 1.2 | [Яндекс Интернетометр](https://yandex.ru/internet/) — VPN выключен. |
| 1.3 | Записать DL / UL / ping. |

**Интерпретация:** DL/UL **50+ Mbps** → линия не виновата в ~15 Mbps через VPN.

---

## 2. Baseline с VPN

| Шаг | Действие |
|-----|----------|
| 2.1 | VPN on, ICE stable. |
| 2.2 | Скопировать `ICE selected` из логов. |
| 2.3 | Ookla (speedtest.net) через VPN. |
| 2.4 | 60 с под нагрузкой: ICE disconnect / restart — да/нет. |

---

## 3. Download-only (curl, гипотеза B5)

Один большой файл, VPN включён:

```powershell
curl.exe -o NUL -w "bytes=%{size_download} time=%{time_total}s`n" `
  "http://mirror.yandex.ru/ubuntu-releases/24.04/ubuntu-24.04.4-netboot-amd64.tar.gz"
```

Mbps ≈ `bytes * 8 / time_total / 1e6`. Сравнить с DL speedtest.

Если `curl: (18)` — обрыв в конце файла: зафиксировать % и логи сервера (`vp8: sent`, `[dc] close`).

---

## 4. Итог фазы 0

1. Заполнить `bench-results/bench_*.txt`.
2. Обновить §9 в THROUGHPUT_HYPOTHESES.
3. Вывод: **лимит линии** / **лимит туннеля** / **обрыв на хвосте большого download**.

**Далее — фаза 1** (код): VP8 scheduler, `VK_VPN_MTU`, ICE relay warn. Старт только после «фаза 0 готова».

---

## Env следующих фаз (не в фазе 0)

| Переменная | Фаза |
|------------|------|
| `VK_VPN_VP8_BATCH=40/50` | 1 |
| `VK_VPN_VP8_PROFILE=aggressive` | 1 |
| `VK_VPN_MTU=1500` | 1 |
| `VK_VPN_DUAL_VP8=1` | 3 |
