# ZHP2PProxy rewrite

## Установка на Linux-сервер

Установщик поддерживает `amd64` и `arm64`, проверяет SHA-256 архива, кладёт
исполняемые файлы в `$HOME/zhp2pproxy`, создает systemd unit и сразу включает
автозапуск:

```bash
curl -fsSL https://link.zeroscan.st/zhp2pproxy/install.sh -o /tmp/install-zhp2pproxy.sh
sudo bash /tmp/install-zhp2pproxy.sh
```

На обычном root-сервере результат находится в `/root/zhp2pproxy`. Для
отдельного системного пользователя укажи его явно:

```bash
sudo ZHP2P_USER=zhproxy bash /tmp/install-zhp2pproxy.sh
```

Повторный запуск обновляет бинарник, но сохраняет `proxy.json`,
`identity.key` и `peers.json`. Предыдущий бинарник остается как
`zhp2pproxy.previous`.

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
Иначе отредактируй `$HOME/zhp2pproxy/proxy.json` и перезапусти службу:

```bash
sudo systemctl restart zhp2pproxy
```

Для публичной relay-ноды открой TCP/UDP `10001`. HTTP `8081` и native tunnel
`3890` по умолчанию остаются доступны только локально.

## Установка на Windows

Поддерживаются 64-битные Windows на `x64` и `ARM64`. Открой PowerShell **от
имени администратора** и выполни:

```powershell
$installer = Join-Path $env:TEMP "install-zhp2pproxy.ps1"
Invoke-WebRequest https://link.zeroscan.st/zhp2pproxy/install.ps1 -OutFile $installer
powershell.exe -NoProfile -ExecutionPolicy Bypass -File $installer
```

Готовые консольные EXE также доступны напрямую:

- `https://link.zeroscan.st/zhp2pproxy/zhp2pproxy-windows-amd64.exe`
- `https://link.zeroscan.st/zhp2pproxy/zhp2pproxy-windows-arm64.exe`

Прямой EXE можно запустить вручную, но для первого запуска рекомендуется
установщик выше: он проверяет SHA-256, создает безопасный `proxy.json`, убирает
старые фоновые способы запуска и регистрирует автозагрузку.

Установщик скачивает архив только по HTTPS, сверяет SHA-256, устанавливает
файлы в `%USERPROFILE%\zhp2pproxy` и запускает сам
`zhp2pproxy.exe` в обычном видимом окне консоли. EXE регистрирует точную команду
с абсолютными путями в
`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, поэтому proxy снова
открывается после входа текущего пользователя в Windows. Старые служба
`ZHP2PProxy`, background task и tray-индикатор останавливаются и отключаются,
чтобы одновременно не работали две копии на одном порту.

В консоли в реальном времени видны загрузка конфига, локальная RPC-нода,
подключение P2P peers, HTTP listener, ошибки и штатная остановка. Тот же вывод
дублируется в `%USERPROFILE%\zhp2pproxy\zhp2pproxy.log`. Закрытие окна или
`Ctrl+C` завершает proxy; при следующем входе в Windows он запустится снова.
При обновлении сохраняются `proxy.json`, `identity.key` и `peers.json`, а
предыдущий бинарник остается как `zhp2pproxy.previous.exe`.

Проверка и управление:

```powershell
Get-ItemProperty "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run" ZHP2PProxy
Get-Content -Wait "$env:USERPROFILE\zhp2pproxy\zhp2pproxy.log"
Invoke-RestMethod http://127.0.0.1:8081/info
Invoke-RestMethod http://127.0.0.1:8081/health
```

Отключить автозапуск можно самим EXE; включить обратно можно аналогично:

```powershell
& "$env:USERPROFILE\zhp2pproxy\zhp2pproxy.exe" -unregister-autostart
& "$env:USERPROFILE\zhp2pproxy\zhp2pproxy.exe" -register-autostart `
  -config "$env:USERPROFILE\zhp2pproxy\proxy.json"
```

`/health` отдельно показывает связь с локальной ZHCASH RPC-нодой и с P2P
overlay. Локальная нода необязательна: при отсутствии `zerohour.conf` proxy
может выполнять разрешенные RPC-вызовы через подключенные P2P peers.
Windows-установщик ждет подключения к overlay и выполняет настоящий
`getblockcount` через локальный HTTP endpoint proxy. Поэтому итоговая
диагностика проверяет запись HKCU Run, живой консольный процесс, `/health`, P2P
peers и настоящий рабочий маршрут `getblockcount` до сети. Если EXE сразу
завершается, установщик показывает последние строки `zhp2pproxy.log`.

Если локальный процесс ZHCASH или его стандартный конфиг обнаружен, но прямой
RPC не отвечает, установщик применяет последний fallback: сохраняет исходный
`zerohour.conf` и `proxy.json` в timestamped backup, удаляет только конфликтующие
RPC-параметры и записывает новый минимальный блок `server=1` с уникальным
паролем и доступом только через `127.0.0.1:3889`. Существующий managed-блок
повторно не меняется. Это единственный случай, когда установщик задает вопрос в
консоли; без подтверждения файлы не меняются. После этого нужно перезапустить
ZHCASH-ноду, затем закрыть и снова открыть `zhp2pproxy.exe`. Если ни процесса, ни конфига локальной
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

Если `proxy.json` отсутствует, бинарник создаст его сам при первом запуске. RPC-доступ к ZHCash node можно задать несколькими способами:

- оставить `rpc_user` / `rpc_pass` пустыми, тогда прокси сначала ищет `zerohour.conf`, читает оттуда credentials и записывает найденный путь в `node_config_path`;
- прописать `node_config_path`, если нужен конкретный `zerohour.conf`;
- прописать `rpc_url`, если RPC удаленный HTTPS endpoint, например `https://rpc.zeroscan.st/`;
- полностью прописать `rpc_host`, `rpc_port`, `rpc_user`, `rpc_pass`, если RPC удаленный без URL или нужен ручной режим. В этом случае автопоиск `zerohour.conf` не перетирает ручные credentials.

Конфиг не зависит от текущей директории запуска. Без `-config` прокси ищет `proxy.json` в таком порядке:

- рядом с самим бинарником;
- в текущей директории;
- в user config dir: `~/.config/zhp2pproxy/proxy.json` на Linux.

Если файл не найден, он создается рядом с бинарником. Относительные пути `identity_key_path`, `peer_store_path` и `node_config_path` считаются относительно директории найденного `proxy.json`. Явный путь всегда можно задать так:

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
- отдельная ZeroTier-подобная сеть прокси через `overlay_network_id`: разные network id получают разные protocol id, DHT rendezvous и pubsub topic;
- private libp2p network через `overlay_private` и `overlay_psk`: чужие узлы без такого же PSK не смогут подключиться к swarm;
- relay-first режим: приватные ноды за NAT подключаются исходящим соединением к публичным relay/bootstrap-нОДам и общаются через `/p2p-circuit`;
- начальная точка входа `link.zeroscan.st`: прокси читает bootstrap peers из `https://link.zeroscan.st/.well-known/zhp2pproxy/peers.json`, а дальше сеть живет через DHT, peer announce и peer cache;
- native binary P2P tunnel: локальный TCP listener `p2p_proxy_listen` проксирует бинарный P2P-протокол ZHCash-ноды через overlay к другим нодам;
- read-only публичные RPC методы распределяются по connected overlay peers, чтобы не нагружать одну RPC-ноду;
- выбор RPC peer идет по рейтингу: успешные ответы, низкая средняя latency, штраф за ошибки и бонус за публичный белый IP;
- опциональные NAT port mapping и hole punching выключены по умолчанию, чтобы не пробивать NAT;
- bootstrap peers из `proxy.json`;
- DHT rendezvous discovery `zhcash-rpc-proxy`;
- P2P fallback через stream protocol `/zhcash/rpc/1.0.0`;
- стабильный peer identity в `identity.key`;
- peer cache в `peers.json`.
- admin API для дебага: `/admin/status`, `/admin/connect`.

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
  "overlay_mode": "relay-first",
  "force_relay": true,
  "relay_enabled": true,
  "enable_ipv6": true,
  "enable_quic": true,
  "enable_nat_port_map": false,
  "enable_hole_punching": false,
  "node_p2p_host": "127.0.0.1",
  "node_p2p_port": 3888,
  "p2p_proxy_listen": "127.0.0.1:3890",
  "native_p2p_proxy": true,
  "local_rpc_prefer_direct": false
}
```

Для публичной ноды меняй `listen` на `:8081`, открывай `p2p_port` TCP/UDP и задавай `public_endpoint`.

## Overlay без пересечения с пользовательским VPN

Это не системный VPN. Прокси не создает сетевой интерфейс, не добавляет route, не меняет DNS и не перехватывает трафик других программ. Все соединения идут только из процесса `zhp2pproxy` через libp2p streams/pubsub.

`overlay_network_id` делает отдельную сеть именно для прокси. Он входит в:

- stream protocol id: `/zhcash/rpc/1.0.0/<network>`;
- DHT rendezvous: `zhcash-rpc-proxy/<network>`;
- GossipSub topic: `zhcash-rpc-tx/<network>`.

`overlay_private: true` включает libp2p private network. Если `overlay_psk` задан, все ноды должны иметь одинаковый 32-byte ключ в hex/base64. Если `overlay_psk` пустой, ключ детерминированно выводится из `overlay_network_id`; это удобно для тестов, но для закрытой сети лучше задать случайный секрет.

Для нод за NAT используй `overlay_mode: "relay-first"` и `force_relay: true`. В этом режиме нода считает себя private, не открывает NAT через UPnP/NAT-PMP и не запускает hole punching. Ей нужен хотя бы один публичный bootstrap/relay peer в `bootstrap_peers`; без публичного relay ноды за разными NAT не смогут гарантированно найти маршрут друг к другу.

Публичная relay/bootstrap-нода должна быть доступна из интернета по TCP/UDP `p2p_port` и иметь `relay_enabled: true`. Приватным нодам входящие порты не нужны: они сами держат исходящее соединение к relay, а остальные прокси обращаются к ним по circuit relay адресу.

## Native P2P proxy для блоков

`native_p2p_proxy: true` включает TCP listener `p2p_proxy_listen`. Локальная ZHCash-нода может подключиться к нему как к обычному peer, например через `addnode=127.0.0.1:3890`. Прокси выбирает connected overlay peer, открывает бинарный stream `/zhcash/node-p2p/1.0.0/<network>` и соединяет два TCP потока без понимания содержимого. На удаленной стороне прокси подключается к своей локальной ZHCash-нОДе `node_p2p_host:node_p2p_port`.

Так блоки и native p2p messages могут идти между ZHCash-нОДами через overlay даже если обе ноды сидят за NAT. Для strict/symmetric NAT используется relay. Если позже захочешь direct-соединения там, где это возможно, можно включить `enable_hole_punching: true`, но гарантированный режим для всех NAT - это relay-first.

## Admin API

Admin API отделен от публичного RPC. Если `admin_token` пустой, доступ разрешен только с loopback. Для удаленного дебага задай `admin_token` в `proxy.json` и передавай его как `X-Admin-Token: <token>` или `Authorization: Bearer <token>`.

```bash
curl http://127.0.0.1:8081/admin/status
curl -X POST http://127.0.0.1:8081/admin/connect \
  -H 'Content-Type: application/json' \
  -d '{"addr":"/ip4/1.2.3.4/tcp/10001/p2p/PEER_ID"}'
```

`/admin/status` показывает network id, peer id, protocol namespace, listen addrs, bootstrap peers, connected peers, known peers, connection addresses, metrics, score, public IP flag, success/failure counters and average RPC latency.

## RPC distribution

RPC stream внутри overlay доступен всем участникам private-сети, но только для методов из whitelist. HTTP-клиент по-прежнему обращается к обычному `POST /`, а прокси сам решает, обслуживать read-only запрос локально или отправить его лучшему peer.

При `p2p_load_balance: true` и `local_rpc_prefer_direct: false` read-only методы идут P2P-first. Peer выбирается по score:

- базовый рейтинг для connected peer;
- бонус за публичный белый IP, потому что такой узел полезен как опорный участник сети;
- бонус за высокий процент успешных ответов;
- бонус за низкую EWMA latency;
- штраф за недавние ошибки и накопленные failures.

Если лучший peer не ответил, прокси пробует следующий. Если P2P не дал результата, запрос падает обратно на локальную RPC-ноду.
