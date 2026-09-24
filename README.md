# ZHP2PProxy

ZHP2PProxy is a constrained RPC and native-node transport for ZHCASH. It lets
local applications reach a local node or an overlay peer without exposing
wallet keys or unrestricted node administration. The project is licensed under
[Apache-2.0](LICENSE). Read [SECURITY.md](SECURITY.md) and
[THREAT_MODEL.md](THREAT_MODEL.md) before operating a public relay.

## Установка на Linux-сервер

Установка последней официальной версии одной строкой:

```bash
curl --proto '=https' --tlsv1.2 -fsSL https://link.zeroscan.st/zhp2pproxy/install.sh | sudo bash
```

Установщик поддерживает `amd64` и `arm64`, получает точную ссылку и SHA-256
исполняемого файла из официального GitHub Releases API, создает отдельного
непривилегированного пользователя `zhp2pproxy`, устанавливает hardened systemd
unit и сразу включает автозапуск:

```bash
curl -fsSL https://link.zeroscan.st/zhp2pproxy/install.sh -o /tmp/install-zhp2pproxy.sh
sudo bash /tmp/install-zhp2pproxy.sh
```

Новая установка находится в `/var/lib/zhp2pproxy`. Уже существующая старая
root-установка сохраняет каталог и пользователя, чтобы обновление не потеряло
доступ к закрытому `zerohour.conf`. Её миграцию выполняй явно после проверки
прав доступа к локальному RPC:

```bash
sudo ZHP2P_USER=zhp2pproxy bash /tmp/install-zhp2pproxy.sh
```

Повторный запуск обновляет бинарник, но сохраняет `proxy.json`,
`identity.key` и `peers.json`. Предыдущий бинарник остается как
`zhp2pproxy.previous`.

Linux-служба автоматически проверяет подписанный
`https://github.com/zerohourcash/zhp2pproxy/releases/latest/download/update.json`
через 20 секунд после запуска
и далее раз в 5 минут. Manifest проверяется встроенным Ed25519 public key,
бинарник — SHA-256 и собственным `-version`; текущий файл сохраняется как
`zhp2pproxy.previous`, новый активируется атомарно, а systemd перезапускает
службу. Отключить удалённые проверки можно в `proxy.json`:

```json
{
  "auto_update_enabled": false
}
```

Интервал можно увеличить через `update_check_interval_seconds` (минимум 300).
Manifest и бинарники принимаются только из GitHub Releases официального
репозитория `zerohourcash/zhp2pproxy`; произвольный `update_manifest_url`
автоматически заменяется официальным.

Текущее состояние обновлятора доступно в полях `auto_update` обоих публичных
диагностических ответов — `GET /health` и `GET /info`. Там видны режим,
текущая и найденная версии, время последней успешной проверки и последняя
ошибка. Режим `automatic` на Linux означает проверку, атомарную установку и
перезапуск через systemd; `disabled` — явное отключение в конфиге. Windows до
публикации Authenticode-подписанного `.exe` работает в безопасном режиме
`notify`: проверяет официальный релиз и выводит ссылку, но не заменяет
исполняемый файл незаметно для пользователя.

Проверка после установки:

```bash
systemctl status zhp2pproxy --no-pager -l
journalctl -u zhp2pproxy -f
curl http://127.0.0.1:8081/info
```

Начальный `proxy.json` подключается к сети `zhcash-proxy-main` через bootstrap
на `link.zeroscan.st`, слушает HTTP API только на loopback и не содержит
приватных RPC credentials. Если локальная ZHCASH-нода использует стандартный
`~/.zerohour/zerohour.conf`, прокси найдет RPC credentials автоматически.

Создаваемый прокси минимальный `zerohour.conf` не содержит комментариев и
включает `txindex=1` вместе с `addrindex=1`. Первый индекс нужен для резервного
чтения подтвержденных транзакций через `getrawtransaction`, второй — для
ограниченной истории произвольного адреса. При первом включении `addrindex`
нода запускается с `-reindex-chainstate`; построение индексов может занять
продолжительное время и дополнительное место на диске. Это работает со
снапшотом, содержащим полные `blocks/blk*.dat`. Для урезанного/pruned-снапшота
сначала нужны отсутствующие блоки. Обычный `scantxoutset` доступен и без
address index.
Иначе отредактируй `$HOME/zhp2pproxy/proxy.json` и перезапусти службу:

```bash
sudo systemctl restart zhp2pproxy
```

Для публичной relay-ноды открой TCP/UDP `10001`. HTTP `8081` и native tunnel
`3890` по умолчанию остаются доступны только локально.

Публичные browser-порты: UDP `10003` для WebTransport и UDP `10004` для
WebRTC Direct. Browser RPC публикует только ограниченный белый список чтения,
`testmempoolaccept` и `sendrawtransaction` уже подписанной транзакции. Если
локальной ZHCASH-ноды нет, proxy пересылает запрос через приватный overlay к
peer с рабочей RPC-нодой; admin/node-control методы браузеру не открываются.
Параметры `browser_webtransport_enabled` и `browser_webrtc_enabled` включены в
установочном конфиге явно; их UDP-порты валидируются и не могут совпадать.
`/info` и `/admin/status` возвращают `browser_rpc_protocol` и готовые
`browser_addrs` с `certhash`. Для подключения новых публичных proxy эти адреса
и, при наличии TLS-домена, `wss://.../zhp2p/rpc` публикуются в каталоге PWA
`zhp2pproxy-browser.json`; кошелёк отбрасывает `ws://` и multiaddr без
закреплённого сертификата.

## Другие блокчейны и автономное управление

Proxy поддерживает Ethereum, Polygon, BNB Chain, opBNB и Solana, включая
указанные в `proxy.json` тестовые сети. Через HTTP запрос отправляется на
`POST /chains/<chain-id>/rpc`; через WSS, WebRTC Direct, WebTransport или
private overlay используется метод `multichain_rpc` с одним envelope:

```json
{"jsonrpc":"2.0","id":1,"method":"multichain_rpc","params":[{"chain":"bsc-mainnet","method":"eth_getBalance","params":["0x...","latest"]}]}
```

Разрешены только безопасные методы чтения, dry run/simulation и отправка уже
подписанной транзакции. Приватные ключи, подпись и unlock-методы через proxy не
проходят. Текущая доступность каждой сети, выбранный публичный host и задержка
видны в `/info` и `/health`; URL с credentials в конфиге запрещены.

Система не зависит от ZeroScan для запуска. Сначала всегда доступны встроенные
RPC и настройки владельца из `proxy.json`, затем — последний проверенный кэш.
`zeroscan.st` является главным центром управления каталогом: раз в 15 минут
proxy запрашивает `/.well-known/zhp2pproxy/chains.json` и его `.sig`. Каталог
применяется только после Ed25519-проверки встроенным public key и только при
росте монотонного `sequence`; он может добавить публичные credential-free HTTPS
RPC, но не заменить локальные настройки и не направить запрос на loopback или
private IP. При недоступности сервера proxy продолжает работу без задержки на
последней конфигурации. Управление можно полностью отключить:

```json
{
  "multichain_catalog_disabled": true
}
```

Адрес, путь к кэшу и интервал задаются параметрами
`multichain_catalog_url`, `multichain_catalog_cache_path` и
`multichain_catalog_refresh_seconds` (минимум 300 секунд). Любой изменённый
сервер всё равно должен публиковать каталог с действующей официальной подписью.

## RPC-журнал и нагрузка

Одинаковый аудит работает в Linux и Windows. Каждое фактическое обращение к
RPC печатает метод, источник, маршрут, адрес локальной RPC-ноды или peer id,
результат и длительность. Например:

```text
[rpc] method=getblockcount source=http-jsonrpc route=local-rpc target=http://127.0.0.1:3889 status=ok duration_ms=4
[rpc] method=getblock source=browser-websocket route=overlay-rpc target=12D3KooW... status=ok duration_ms=83
```

Раз в час печатается статистика именно за прошедшее часовое окно: количество
запросов, успешных ответов и ошибок, средняя и максимальная задержка, нагрузка
по маршрутам, источникам и RPC-методам. При штатной остановке дополнительно
печатается неполное текущее окно:

```text
[rpc-stats] window=1h0m0s requests=420 ok=416 errors=4 avg_ms=18 max_ms=907 routes=local-rpc:380/2,overlay-rpc:40/2 sources=http-jsonrpc:300,node-monitor:120 methods=getblock:80,getblockcount:340
```

Параметры, request id, тела ответов, raw-транзакции и RPC credentials никогда
не записываются в этот журнал. В Linux строки доступны через
`journalctl -u zhp2pproxy -f`, а в Windows видны в консоли и дублируются в
`%APPDATA%\ZHCASH\zhp2pproxy\zhp2pproxy.log`.
Команда `zhp2pproxy -version` показывает и номер версии, и монотонный release
sequence, по которому автоматическое обновление сравнивает сборки.

## Установка на Windows

Код поддерживает 64-битные Windows на `x64` и `ARM64`. Официальные EXE доступны
в GitHub Releases по адресам:

- `https://github.com/zerohourcash/zhp2pproxy/releases/latest/download/zhp2pproxy-windows-amd64.exe`
- `https://github.com/zerohourcash/zhp2pproxy/releases/latest/download/zhp2pproxy-windows-arm64.exe`

EXE сам копирует себя в
`%USERPROFILE%\zhp2pproxy\zhp2pproxy.exe`, продолжает
работу из постоянной папки, создает runtime-каталог рядом с данными ZHCASH-ноды
и регистрирует автозагрузку текущего пользователя. Исходный файл в Downloads
после этого больше не нужен.

Расширенный `install.ps1` остается дополнительным вариантом для миграции старой
Windows-службы и полной диагностики, но для обычного первого запуска он не
требуется.

Текущий выпуск проверяется SHA-256 digest в GitHub Releases. Пока проект не
получил доверенный Authenticode-сертификат, Windows может показать SmartScreen;
автоматическая тихая замена EXE поэтому отключена, а обновлятор работает в
режиме `notify`. После ручного запуска `zhp2pproxy.exe` работает в обычном
видимом окне консоли и регистрирует точную команду
с абсолютными путями в
`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, поэтому proxy снова
открывается после входа текущего пользователя в Windows. Старые служба
`ZHP2PProxy`, background task и tray-индикатор останавливаются и отключаются,
чтобы одновременно не работали две копии на одном порту.

В консоли в реальном времени видны загрузка конфига, локальная RPC-нода,
подключение P2P peers, HTTP listener, ошибки и штатная остановка. Тот же вывод
дублируется в `%APPDATA%\ZHCASH\zhp2pproxy\zhp2pproxy.log`. Закрытие окна или
`Ctrl+C` завершает proxy; при следующем входе в Windows он запустится снова.
При обновлении сохраняются `proxy.json`, `identity.key` и `peers.json`, а
предыдущий бинарник остается как `zhp2pproxy.previous.exe`.

Windows проверяет тот же подписанный manifest, но не заменяет работающий EXE.
Когда доступна новая версия, в видимой консоли появляются номер версии и прямая
HTTPS-ссылка на подходящий `amd64` или `arm64` EXE. Пользователь скачивает и
открывает файл; встроенная установка сама обновляет постоянную копию. Проверку
можно отключить через `"auto_update_enabled": false`.

Если ZHCASH-нода запущена, но ее локальный RPC не отвечает, EXE после повторной
проверки сам сохраняет timestamped backup `zerohour.conf`, заменяет только
конфликтующие RPC-параметры на loopback-only набор для `127.0.0.1:3889`,
добавляет `txindex=1` и `addrindex=1`, удаляет из записываемого конфига все
комментарии, синхронизирует `proxy.json`, штатно завершает найденный процесс
ноды и запускает тот же node EXE снова. При первом включении address index к
запуску добавляется `-reindex-chainstate`. Проверка RPC продолжается каждые 10
секунд; появление или потеря соединения отражается в консоли и журнале.
Принудительное завершение
`/F` не используется: если нода не остановилась безопасно за 3 минуты, proxy
оставляет ее запущенной и сообщает ошибку.

Все runtime-данные Windows находятся рядом с данными ZHCASH-ноды в одной
постоянной папке `%APPDATA%\ZHCASH\zhp2pproxy`: `proxy.json`,
`proxy.example.json`, `peers.json`, `identity.key` и `zhp2pproxy.log`. Место
запуска EXE больше не влияет на выбор конфига. При первом запуске валидные старые
данные рядом с EXE автоматически копируются в эту папку; для общей сети
`zhcash-proxy-main` одноразовый случайный PSK заменяется общим сетевым ключом.

Проверка и управление:

```powershell
Get-ItemProperty "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run" ZHP2PProxy
Get-Content -Wait "$env:APPDATA\ZHCASH\zhp2pproxy\zhp2pproxy.log"
Invoke-RestMethod http://127.0.0.1:8081/info
Invoke-RestMethod http://127.0.0.1:8081/health
```

Отключить автозапуск можно самим EXE; включить обратно можно аналогично:

```powershell
& "$env:USERPROFILE\zhp2pproxy\zhp2pproxy.exe" -unregister-autostart
& "$env:USERPROFILE\zhp2pproxy\zhp2pproxy.exe" -register-autostart `
  -config "$env:APPDATA\ZHCASH\zhp2pproxy\proxy.json"
```

`/health` отдельно показывает связь с локальной ZHCASH RPC-нодой, overlay и
native P2P: доступность локального P2P-порта, число активных туннелей,
успешные/ошибочные `version`-handshake, версии протокола и последнюю ошибку.
Локальная нода необязательна: при отсутствии `zerohour.conf` proxy
может выполнять разрешенные RPC-вызовы через подключенные P2P peers.
Windows-установщик ждет подключения к overlay и выполняет настоящий
`getblockcount` через локальный HTTP endpoint proxy. Поэтому итоговая
диагностика проверяет запись HKCU Run, живой консольный процесс, `/health`, P2P
peers и настоящий рабочий маршрут `getblockcount` до сети. Если EXE сразу
завершается, установщик показывает последние строки `zhp2pproxy.log`.

Если локальный процесс ZHCASH или его стандартный конфиг обнаружен, но прямой
RPC не отвечает, установщик применяет последний fallback: сохраняет исходный
`zerohour.conf` и `proxy.json` в timestamped backup, удаляет только конфликтующие
RPC-параметры и записывает новый минимальный набор `server=1` с уникальным
паролем и доступом только через `127.0.0.1:3889`. Конфиг записывается без
комментариев, а готовый набор RPC/index-параметров повторно не меняется. Это
единственный случай, когда установщик задает вопрос в консоли; без подтверждения
файлы не меняются. После этого нужно один раз запустить ZHCASH-ноду с
`-reindex-chainstate`, затем закрыть и снова открыть `zhp2pproxy.exe`. Если ни процесса, ни конфига локальной
ноды нет, установщик ничего не создает: это штатный P2P-only режим. Для
автоматической установки поведение можно явно выбрать параметром
`-NodeConfigRepair Apply` или `-NodeConfigRepair Skip`.

Для публичной relay-ноды отдельно разреши входящие TCP и UDP подключения на
порт `10001` в Windows Firewall. HTTP `8081` и native tunnel `3890` по
умолчанию доступны только локально.

Новая реализация находится в исходниках Go и собирается обычным способом:

```bash
./build.sh
```

Публичный Windows-релиз должен быть подписан Authenticode. `release.sh` не
публикует Windows EXE в `PUBLISH_DIR`, пока не задан исполняемый
`WINDOWS_SIGN_COMMAND`, принимающий путь к EXE и подписывающий его на месте, и
`WINDOWS_VERIFY_COMMAND`, независимо проверяющий готовую подпись.
Локальную неподписанную сборку можно сделать только для тестов; публичного
override нет. SHA-256 с того же web-сервера проверяет повреждение загрузки, но
не заменяет подпись издателя при компрометации сервера.

Если `proxy.json` отсутствует, бинарник создаст его сам при первом запуске. RPC-доступ к ZHCash node можно задать несколькими способами:

- оставить `rpc_user` / `rpc_pass` пустыми, тогда прокси сначала ищет `zerohour.conf`, читает оттуда credentials и записывает найденный путь в `node_config_path`;
- прописать `node_config_path`, если нужен конкретный `zerohour.conf`;
- прописать `rpc_url`, если RPC удаленный HTTPS endpoint, например `https://rpc.zeroscan.st/`;
- полностью прописать `rpc_host`, `rpc_port`, `rpc_user`, `rpc_pass`, если RPC удаленный без URL или нужен ручной режим. В этом случае автопоиск `zerohour.conf` не перетирает ручные credentials.

Конфиг не зависит от текущей директории запуска. На Windows без `-config`
всегда используется `%APPDATA%\ZHCASH\zhp2pproxy\proxy.json`; старый валидный
конфиг рядом с EXE мигрируется туда автоматически. На Linux поиск остается
совместимым: рядом с бинарником, в текущей директории, затем
`~/.config/zhp2pproxy/proxy.json`.

Относительные пути `identity_key_path`, `peer_store_path` и `node_config_path`
считаются относительно директории найденного `proxy.json`. Явный путь всегда
можно задать так:

```bash
./zhp2pproxy -config /opt/zhp2pproxy/proxy.json
```

Автопоиск `zerohour.conf` проверяет:

- директорию найденного `proxy.json`;
- директорию самого бинарника;
- текущую директорию запуска;
- стандартный путь ОС, например `~/.zerohour/zerohour.conf` на Linux.

Основные возможности:

- HTTP JSON-RPC endpoint: `POST /`;
- status endpoint: `GET /info`;
- pubsub publish endpoint: `POST /pubsub/publish`;
- whitelist публичных RPC-методов;
- batch JSON-RPC до 100 запросов;
- per-IP rate limit, API token, IP allowlist, CORS;
- libp2p TCP/QUIC listen addresses для IPv4 и IPv6;
- proxy-only overlay: приложение строит свою private P2P-сеть без TUN/TAP, без изменения системных маршрутов, DNS и текущего VPN пользователя;
- отдельная ZeroTier-подобная сеть прокси через `overlay_network_id`: разные network id получают разные protocol id и подписанный pubsub topic;
- libp2p pnet separation через `overlay_private` и `overlay_psk`; детерминированный ключ общей сети публичен и не считается контролем доступа;
- адаптивный NAT traversal: AutoNAT определяет доступность, QUIC/TCP сначала
  строят прямой путь, DCUtR пробивает NAT через уже работающий relay-канал, а
  circuit relay остается гарантированным резервом;
- начальная точка входа `link.zeroscan.st`: прокси читает bootstrap peers только по HTTPS, использует их как AutoRelay-кандидатов, а дальше сеть живет через подписанные peer announce и локальный peer cache; уязвимый Kademlia DHT не используется;
- native binary P2P tunnel: локальный TCP listener `p2p_proxy_listen` проксирует только соединения, начавшиеся с корректного ZHCASH mainnet `version` (`f1cfa6d3` и валидная checksum), и подтверждает version-exchange в `/health`;
- read-only публичные RPC методы распределяются по connected overlay peers, чтобы не нагружать одну RPC-ноду;
- выбор RPC peer идет по рейтингу: успешные ответы, низкая средняя latency, штраф за ошибки и бонус за публичный белый IP;
- опциональные NAT port mapping и hole punching выключены по умолчанию, чтобы не пробивать NAT;
- bootstrap peers из `proxy.json`;
- P2P fallback через stream protocol `/zhcash/rpc/1.0.0`;
- стабильный peer identity в `identity.key`;
- peer cache в `peers.json`.
- admin API для дебага: `/admin/status`, `/admin/connect`, `/admin/probe`.

Минимальный приватный конфиг:

```json
{
  "listen": "127.0.0.1:8081",
  "p2p_port": 0,
  "public_endpoint": "",
  "rpc_url": "",
  "rpc_host": "127.0.0.1",
  "rpc_port": 3889,
  "rpc_user": "USER",
  "rpc_pass": "PASS",
  "bootstrap_peers": ["/ip4/1.2.3.4/tcp/10001/p2p/PEER_ID"],
  "bootstrap_urls": ["https://link.zeroscan.st/.well-known/zhp2pproxy/peers.json"],
  "overlay_network_id": "my-zhcash-overlay",
  "overlay_private": true,
  "overlay_psk": "32-byte-key-as-64-hex-chars",
  "overlay_mode": "adaptive",
  "force_relay": false,
  "relay_enabled": false,
  "enable_ipv6": true,
  "enable_quic": true,
  "enable_nat_port_map": false,
  "enable_hole_punching": true,
  "nat_traversal_enabled": true,
  "node_p2p_host": "127.0.0.1",
  "node_p2p_port": 38100,
  "p2p_proxy_listen": "127.0.0.1:3890",
  "native_p2p_proxy": true,
  "node_p2p_magic": "f1cfa6d3",
  "p2p_max_tunnels": 64,
  "p2p_max_tunnels_per_peer": 4,
  "p2p_max_bytes_per_second": 8388608,
  "p2p_rpc_rate_limit": 5,
  "p2p_rpc_burst": 10,
  "p2p_rpc_max_concurrent": 8,
  "p2p_rpc_quorum": 2,
  "local_rpc_prefer_direct": false
}
```

Для публичной ноды открывай `p2p_port` TCP/UDP и browser transport ports.
Оставляй `listen` и `p2p_proxy_listen` на loopback. Если удалённый HTTP API
действительно необходим, ставь TLS reverse proxy, непустой `api_token` и
`allowed_ips`; CORS не заменяет аутентификацию.

## Overlay без пересечения с пользовательским VPN

Это не системный VPN. Прокси не создает сетевой интерфейс, не добавляет route, не меняет DNS и не перехватывает трафик других программ. Все соединения идут только из процесса `zhp2pproxy` через libp2p streams/pubsub.

`overlay_network_id` делает отдельную сеть именно для прокси. Он входит в:

- stream protocol id: `/zhcash/rpc/1.0.0/<network>`;
- GossipSub topic: `zhcash-rpc-tx/<network>`.

`overlay_private: true` включает libp2p private network. Если `overlay_psk` задан, все ноды должны иметь одинаковый 32-byte ключ в hex/base64. Если `overlay_psk` пустой, ключ детерминированно выводится из `overlay_network_id`. Такой общий ключ отделяет протокол от других libp2p-сетей, но не является контролем доступа: исходный код и network id публичны. Для действительно закрытой сети нужен случайный секретный PSK, доставленный участникам отдельно.

Обычная пользовательская нода использует `overlay_mode: "adaptive"`,
`nat_traversal_enabled: true` и `relay_enabled: false`. AutoNAT определяет
фактическую доступность. Если прямой входящий путь отсутствует, AutoRelay
держит резервирование сразу на двух relay, а DCUtR продолжает пытаться
перевести соединение в прямой TCP/QUIC. Каждые две минуты прокси дополнительно
проверяет публичные адреса relayed peers и безопасно повышает путь до direct,
не обращаясь к частным и loopback-адресам.

`enable_nat_port_map` по умолчанию выключен: UPnP/NAT-PMP можно разрешить
отдельно только в доверенной домашней сети. Для сети, где прямые соединения
запрещены политикой, остаются явные `overlay_mode: "relay-only"` или
`force_relay: true`; они отключают NAT traversal.

Для массового запуска peer announcements обрабатываются ограниченным пулом
dial-workers: сотни одновременных объявлений не блокируют GossipSub и не
создают неограниченное число goroutine. Bootstrap-подключения выполняются
параллельно с фиксированным пределом, поддерживаются минимум два входных пути,
а настроенный HTTPS-каталог имеет приоритет перед локальным кэшем.

Публичная relay/bootstrap-нода должна быть доступна из интернета по TCP/UDP
`p2p_port`, иметь `overlay_mode: "public-relay"` и
`relay_enabled: true`. Приватным нодам входящие порты не нужны.
Relay имеет собственные пределы: до 128 reservations, до 8 circuits на peer,
до 4 reservations с одного IP и не более 1 GiB/6 часов на одно relayed
соединение. Native-туннель дополнительно ограничивается по peers, числу
соединений и скорости.

## Native P2P proxy для блоков

`native_p2p_proxy: true` включает TCP listener `p2p_proxy_listen`. Локальная ZHCash-нода может подключиться к нему как к обычному peer, например через `addnode=127.0.0.1:3890`. Прокси выбирает peer, который объявил доступную локальную ноду, открывает stream `/zhcash/node-p2p/1.0.0/<network>` и проверяет первый `version` в обе стороны: mainnet magic `f1 cf a6 d3`, команду, размер payload и двойную SHA-256 checksum. Только после успешного version-exchange начинается прозрачная передача остальных native сообщений.

Удаленная сторона может подключаться только к loopback `node_p2p_host`; превратить overlay в прокси до произвольного адреса нельзя. Основной mainnet-порт — `38100`. Для миграции старых конфигов автоматически проверяются также ранее использованные `8003` и `3888`, а фактически найденный адрес показывается в `/health`. Число туннелей ограничено глобально и на один Peer ID, трафик каждого направления ограничен `p2p_max_bytes_per_second`.

Так блоки и native p2p messages идут между ZHCash-нодами напрямую, когда NAT
это допускает, и автоматически возвращаются на relay при strict/symmetric NAT.
`/info` и `/admin/status` показывают `nat_traversal`: reachability, тип
NAT для TCP/UDP, число direct/relay peers, используемые транспорты, результаты
hole punching и периодического direct-upgrade.

## Admin API

Admin API отделен от публичного RPC. При первом чтении конфига proxy генерирует уникальный `admin_token`, если поле отсутствует или пустое. Конфиг и identity имеют owner-only доступ. Передавай токен как `X-Admin-Token: <token>` или `Authorization: Bearer <token>`.

```bash
ADMIN_TOKEN="$(jq -r .admin_token ./proxy.json)"
curl -H "X-Admin-Token: $ADMIN_TOKEN" http://127.0.0.1:8081/admin/status
curl -X POST http://127.0.0.1:8081/admin/connect \
  -H 'Content-Type: application/json' \
  -H "X-Admin-Token: $ADMIN_TOKEN" \
  -d '{"addr":"/ip4/1.2.3.4/tcp/10001/p2p/PEER_ID"}'
curl -X POST http://127.0.0.1:8081/admin/probe \
  -H 'Content-Type: application/json' \
  -H "X-Admin-Token: $ADMIN_TOKEN" \
  -d '{"peer_id":"PEER_ID","method":"getblockcount","params":[]}'
```

`/admin/status` показывает network id, peer id, protocol namespace, listen addrs, bootstrap peers, connected peers, known peers, connection addresses, metrics, score, public IP flag, success/failure counters and average RPC latency.
`/admin/probe` выполняет разрешенный RPC-метод через строго указанный
подключенный peer для end-to-end проверки конкретной ноды.

## RPC distribution

RPC stream внутри overlay доступен участникам сети, но имеет отдельный узкий whitelist, per-peer rate limit, общий concurrency limit и ограничение размера ответа. Удалённый `scantxoutset` разрешён только как fallback после ошибки локальной RPC: действие строго `start`, не более двух `addr(Z...)`/`combo(compressed-pubkey)` descriptors, один скан одновременно и не чаще одного запуска за 15 секунд на принимающей proxy. `raw`, диапазоны, xpub/xprv, `status` и `abort` через overlay запрещены. Также удалённо запрещены обслуживание chain state, выбор цепочки, сохранение mempool и операции подписи. `sendrawtransaction` сохранён для передачи уже подписанных транзакций. HTTP-клиент по-прежнему обращается к обычному `POST /`, а прокси сам решает, обслуживать запрос локально или отправить его лучшему peer.

HTTP API отклоняет неизвестный browser `Origin`, требует `application/json` для RPC/admin POST и доверяет `X-Forwarded-For` только адресам из `trusted_proxies`. Незашифрованный RPC разрешен лишь до loopback-ноды; удаленный endpoint обязан использовать HTTPS.

При `p2p_load_balance: true` и `local_rpc_prefer_direct: false` read-only методы идут P2P-first. Peer выбирается по score:

- базовый рейтинг для connected peer;
- бонус за публичный белый IP, потому что такой узел полезен как опорный участник сети;
- бонус за высокий процент успешных ответов;
- бонус за низкую EWMA latency;
- штраф за недавние ошибки и накопленные failures.

Если лучший peer не ответил, прокси пробует следующий. Если P2P не дал результата, запрос падает обратно на локальную RPC-ноду.

Для детерминированных данных блоков, подтверждённых транзакций, proofs,
contract code/storage и transaction receipts при наличии не менее
`p2p_rpc_quorum` подключённых peers ответы канонизируются и должны совпасть.
Значение по умолчанию — `2`, максимум — `3`. При единственном доступном peer
fallback сохраняется, но его ответ не считается независимо подтверждённым и
должен дополнительно проверяться кошельком.

## Разработка, безопасность и релизы

Перед изменениями прочитай [THREAT_MODEL.md](THREAT_MODEL.md). Полный локальный
набор проверок запускается одной командой:

```bash
./scripts/ci.sh
```

Он проверяет форматирование, `go mod tidy`, race detector, vet, тесты,
cross-build Linux/Windows и repository hygiene; при наличии также запускает
Staticcheck, Govulncheck и Gitleaks. CI устанавливает закреплённые версии этих
инструментов. Порядок официальной публикации, подписи и постепенного rollout
описан в [docs/OFFICIAL_RELEASE.md](docs/OFFICIAL_RELEASE.md).

Лицензия: [Apache-2.0](LICENSE). Сообщения об уязвимостях отправляй только через
private vulnerability reporting согласно [SECURITY.md](SECURITY.md).
