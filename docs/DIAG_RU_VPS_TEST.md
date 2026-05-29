# Диагностический тест: RU VPS — локализация cap'а

**Цель:** разделить три сценария по форме нашего ~1.2 Mbps cap'а:

| Сценарий | Что значит | Если подтвердится |
|---|---|---|
| **A** — глобальный кап TURN ВК | VK режет всех на канал, независимо от IP | Транспортный фикс невозможен. Pivot на SFU-сервис или принимаем |
| **B.1** — geo-shaping иностранных AS | VK включает шейпер для пакетов на Hetzner/DigitalOcean/etc. | Переезд на RU VPS даст высокую скорость |
| **B.2** — физика RTT × packet loss | International RTT 100-150ms × 3-6% loss → KCP underutilization | KCP tuning + RU intermediate VPS |

**Маршрут теста:** `Дом РФ → VK TURN → RU VPS (час) → Дом РФ`

**Расходы:** ~50-200 руб за час российского VPS с любого хостинга в белых списках (Timeweb, Yandex Cloud, ruvds, beget).

---

## Что собрано на этой ветке

Ветка: `diag/ru-vps-test`

Запечено в бинарь (без env-флагов):
- WRAP layer — **всегда ON** (HARD-CODED, env override игнорируется)
- RTCP defaults — **всегда ON** (HARD-CODED)
- Boot-banner — печатает диаг-предупреждение, чтобы случайно не задеплоить в прод
- Клиент принимает **и `myvpn://...`, и `https://vk.com/call/join/<token>`** — для копипасты из логов сервера без бота

Готовый бинарь: `dist/vk-vpn-daemon-diag-linux-amd64` (12 MB, ELF x64 statically linked).

---

## Подготовка к тесту

### 1. Купить RU VPS на час

Любой хостинг внутри РФ, минимальная конфигурация (1 vCPU, 1 GB RAM, ~10 GB SSD). Чек-лист требований:
- **OS:** Ubuntu 22.04 / Debian 12 (другие тоже подойдут, но инструкция под эти)
- **Доступ:** SSH с твоим ключом
- **Сеть:** прямой публичный IPv4 без NAT
- **Локация:** **строго РФ** (Москва или СПб для минимального RTT до VK инфраструктуры)

Подойдут:
- Timeweb Cloud (от 49 руб/час Москва)
- Cloud.ru (бывший Sber Cloud)
- Yandex Cloud (compute optimized small)
- ruvds, beget, firstvds

### 2. VK cookies (нужны на RU VPS)

Сервер использует свою VK сессию для создания звонка. **Не запускай его на VPS с тем же IP что и продовый сервер** — это не разделит сценарии.

Берём cookies с твоего продового VPS (где они уже залогинены и работают):

```bash
# На твоём текущем (продовом) VPS:
scp /opt/vk-vpn/vk-vpn-server/cookies.json local-machine:/tmp/cookies.json
```

Или экспортируем заново через Wails-клиент на твоей Windows-машине: открой VK в браузере → залогинься → нажми "Export VK Cookies" в Wails-клиенте → получишь `cookies.json`.

### 3. SCP бинаря и куков на RU VPS

```powershell
# С твоей Windows-машины (PowerShell):
$RU_VPS = "root@<твой-ru-vps-ip>"
scp C:\VPN\vk-vpn\dist\vk-vpn-daemon-diag-linux-amd64 ${RU_VPS}:/root/
scp C:\VPN\vk-vpn\dist\cookies.json ${RU_VPS}:/root/    # или путь где у тебя cookies лежат
```

---

## Запуск на RU VPS

```bash
ssh root@<твой-ru-vps-ip>
chmod +x /root/vk-vpn-daemon-diag-linux-amd64

# Запуск с unlimited ресурсами (надо максимально утилизировать канал)
/root/vk-vpn-daemon-diag-linux-amd64 \
  --cookies=/root/cookies.json \
  --port=8080 \
  --resources=unlimited \
  --log-file=/root/vk-vpn-diag.log
```

После запуска в логе будет:
```
*** DIAG BUILD — branch diag/ru-vps-test ***
[boot] log file: /root/vk-vpn-diag.log
[creator] turn-wrap: installed (local=...)
[creator] rtcp-defaults: installed (NACK + TWCC + RR/SR)
[vk-ws] connected
[vk-ws] joined call <id>
[create-link] join-link: https://vk.com/call/join/XXXXXXXXXXX
```

**Скопируй последнюю строку с `https://vk.com/call/join/...` — её вставишь в клиент.**

Если не видишь её сразу — она появляется когда захочется (после полной инициализации звонка). Подожди 10-30 секунд и посмотри:
```bash
tail -f /root/vk-vpn-diag.log
```

---

## Запуск клиента (твоя Windows)

1. **Собрать Wails-клиент из этой ветки** (если ещё не собран):
   ```powershell
   cd C:\VPN\vk-vpn\vk-client
   wails build
   # Бинарь: build\bin\vk-client.exe
   ```

   Или используй текущую инсталляцию, если она уже с этой ветки.

2. **Запустить клиент**, **вставить raw VK ссылку** из лога сервера в поле ввода.

   Клиент принимает и `myvpn://...` (продовый формат), и `https://vk.com/call/join/...` (диаг формат). Никаких env-переменных не нужно — это ветка для теста, поведение зашито в код.

3. Нажать **Connect**, дождаться `Tunnel active` / `TUNNEL CONNECTED`.

---

## Замер throughput

### Быстрый замер (твой текущий способ)

```powershell
$env:HTTPS_PROXY = "socks5h://127.0.0.1:1080"  # или твой обычный SOCKS5 порт
curl.exe -o NUL https://speed.hetzner.com/100MB.bin
```

Записать **avg KB/s** из вывода.

### С разверсткой посекундно (новый скрипт)

```powershell
cd C:\VPN\vk-vpn
.\scripts\measure-throughput.ps1 -Url "https://speed.hetzner.com/100MB.bin" -SocksPort 1080
```

Скрипт пишет KB/s каждую секунду, в конце даёт средние / медиану / max burst.

---

## Минимум 3 прогона

Cap не статичный, нужно усреднить. **Не закрывай туннель между прогонами** — VK при reconnect'е может поменять TURN сервер, что повлияет на результат.

```
[прогон 1] записать avg KB/s
[прогон 2] записать avg KB/s
[прогон 3] записать avg KB/s
```

---

## Интерпретация результата

| Sustained avg (среднее 3 прогонов) | Вывод | Cap источник |
|---|---|---|
| **≥ 5 Mbps** | Cap не в TURN-инфраструктуре. В РФ-РФ маршруте — нет шейпинга | B.1 (geo-shaping) или B.2 (RTT/loss). RU VPS = победа |
| **2-5 Mbps** | Промежуточный кап. Возможно, частичный шейпинг или KCP под загрузкой | B.2 наиболее вероятно. Тюнинг KCP, но не radical fix |
| **~1.2 Mbps как обычно** | Глобальный TURN cap у VK для всех | **A — путь закрыт.** Pivot на SFU-сервис или принимаем 1.2 Mbps как baseline |
| **< 1 Mbps** | Регрессия или неудачный TURN | Перезапустить, проверить логи, повторить |

---

## После теста

1. **Записать результаты в `bench-results/diag-ru-vps/RESULTS.md`** (3 прогона + интерпретация + длительность теста + IP VPS).
2. **Удалить VK cookies с RU VPS** (`shred -u /root/cookies.json`).
3. **Удалить VPS** (если был часовой).
4. На основе результата — следующая фаза:
   - A → продуктовый pivot (UI/UX/multiservice/etc)
   - B.1 → план переноса прод-сервера на RU хостинг + проверка не блокирует ли VK свои собственные cloud AS
   - B.2 → KCP-окно эксперимент + intermediate RU hop

---

## Troubleshooting

### "VK API error: captcha required"
Cookies устарели или VK не доверяет новому IP. Решения:
1. Заново экспортировать cookies через Wails клиента на твоей машине → SCP на VPS
2. Если регулярно — нужен `headless-vk-creator --browser-captcha-port` (см. whitelist-bypass docs), но для часового теста проще обновить cookies

### "TURN allocate timeout"
RU VPS не пробивает до VK TURN. Возможные причины:
1. Хостинг блокирует UDP egress (некоторые корпоративные хостинги это делают)
2. VK заблокировал твою VPS-сеть как abuser

Попробовать другой хостинг или другой регион.

### Клиент отказывается принимать сырую ссылку
Проверь, что собрал клиент **из этой ветки** (`diag/ru-vps-test`), а не из main. На main клиент требует строго `myvpn://...`. Проверка:
```powershell
git -C C:\VPN\vk-vpn branch --show-current
# должно: diag/ru-vps-test
```

### Throughput неровный или близкий к 0
Перезапустить сервер на VPS (он создаст новую звонок-сессию с новым TURN-портом). VK иногда залипает на старой аллокации.
