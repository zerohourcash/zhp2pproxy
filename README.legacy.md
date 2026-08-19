# ZHP2PProxy — Децентрализованный RPC-прокси для ZHCash

**ZHP2PProxy** — это mesh/P2P-сеть на Go, построенная поверх `go-libp2p`. Она объединяет компьютеры в единую децентрализованную сеть, где каждый узел может выступать как публичный RPC-шлюз, приватный релей или полноценная нода за NAT.

## Возможности

- **Полная прозрачность JSON-RPC** — внешние программы не отличают прокси от прямого обращения к ноде.
- **NAT Traversal** — hole punching, circuit relay, UPnP/NAT-PMP. Ноды за NAT и Windows участвуют в сети наравне с публичными.
- **DHT Discovery** — автоматический поиск пиров через Kademlia DHT без центрального сервера.
- **GossipSub** — broadcast сообщений (транзакции, блоки) по всей сети.
- **Stream-based RPC Fallback** — если локальная нода недоступна, запрос автоматически уходит к случайному пиру через защищённый libp2p-стрим.
- **RPC capabilities** — ноды автоматически сообщают, какие RPC-методы реально доступны у их локальной ZHCash ноды.
- **P2P load balancing** — read-only/cacheable RPC могут обслуживаться подходящими пирами, чтобы распределять нагрузку по сети.
- **Encrypted peer cache** — найденные P2P peers сохраняются локально в зашифрованном `peers.json`, чтобы переживать падение bootstrap-ноды.
- **PWA/browser RPC** — CORS позволяет кошелькам на Vite JS/PWA использовать прокси как обычный JSON-RPC endpoint.
- **Update notifications** — прокси периодически проверяет официальный GitHub `zerohourcash/zerohourcash` и рекомендует обновить ZHCash ноду при появлении нового релиза.
- **Кэш по блоку** — read-only методы кэшируются и инвалидируются при смене высоты блокчейна.
- **Scantxoutset с очередью** — только 1 запрос одновременно, кэш 10 минут.
- **Безопасность** — жёсткий whitelist методов, per-IP rate limit, API token, IP allowlist, отсутствие wallet-методов.

---

## Архитектура

```
┌─────────────────────────────────────────────────────────────┐
│                        P2P Overlay                          │
│  ┌──────────────┐      DHT / GossipSub      ┌──────────┐  │
│  │ Публичная    │◄─────────────────────────►│ Приватная│  │
│  │ нода (VPS)   │      Hole Punching        │ нода     │  │
│  │              │◄─────────────────────────►│ (NAT/Win)│  │
│  └──────┬───────┘                           └────┬─────┘  │
│         │                                        │         │
│    Relay / Fallback                    Relay / Fallback     │
│         │                                        │         │
│  ┌──────▼───────┐                           ┌────▼─────┐  │
│  │ Локальная    │                           │ Локальная│  │
│  │ ZHCash нода  │                           │ ZHCash   │  │
│  └──────────────┘                           └──────────┘  │
└─────────────────────────────────────────────────────────────┘
```

### Компоненты

| Компонент | Описание |
|-----------|----------|
| **libp2p Host** | TCP/QUIC транспорт, Noise/TLS шифрование, NAT service |
| **DHT (Kademlia)** | `ModeServer` для публичных, `ModeClient` для приватных. Rendezvous: `zhcash-rpc-proxy` |
| **Circuit Relay** | Публичные ноды ретранслируют трафик для приватных |
| **Hole Punching** | Direct Connection Upgrade through Relay (DCUtR) |
| **GossipSub** | Топик `zhcash-rpc-tx` для broadcast сообщений |
| **Stream Protocol** | `/zhcash/rpc/1.0.0` — JSON-RPC поверх libp2p streams |
| **Capabilities Protocol** | `/zhcash/rpc-capabilities/1.0.0` — список RPC-методов, доступных у конкретного пира |
| **HTTP Gateway** | `POST /` — JSON-RPC, `GET /info` — состояние ноды |

---

## Требования

- **Go** 1.23+
- **ZHCash нода** (`zerohourd`) с включённым RPC (`server=1`)
- Доступ в интернет (исходящие TCP соединения)

---

## Установка и сборка

```bash
git clone <repo>
cd zhp2pproxy
./build.sh
```

Результат — статический бинарник `./zhp2pproxy`, который можно просто скопировать на любой сервер.

---

## Быстрый запуск новой прокси-ноды

Главная входная точка сети:

```text
https://rpc.zeroscan.st/
```

Bootstrap multiaddr первой публичной ноды:

```text
/ip4/195.200.19.162/tcp/10001/p2p/12D3KooWQMzedrbAyUxu1m8kaJsLEQUMRbES7emZSbz37ShdBpx6
```

### 1. Подготовь ZHCash node

На сервере должна работать `zerohourd` с RPC:

```ini
server=1
rpcuser=USER
rpcpassword=PASS
rpcport=3889
txindex=1
```

`txindex=1` рекомендуется: без него нода не будет объявлять полноценную поддержку `getrawtransaction`.

### 2. Скопируй прокси

```bash
mkdir -p /opt/zhp2pproxy
cp zhp2pproxy /opt/zhp2pproxy/
cp proxy.bootstrap.json /opt/zhp2pproxy/proxy.json
```

Если сервер будет публичной RPC-точкой для кошельков, укажи свой домен:

```json
"public_endpoint": "https://rpc2.example.org"
```

Если это приватная/NAT-нода, оставь:

```json
"public_endpoint": ""
```

### 3. Открой порты

Для публичной ноды:

```text
8081/tcp   HTTP JSON-RPC backend, обычно за HTTPS reverse proxy
10001/tcp  libp2p TCP
10002/tcp  libp2p WebSocket
10003/udp  WebTransport
10004/udp  WebRTC Direct
```

Для приватной NAT-ноды входящие порты не обязательны, но если они доступны, сеть будет работать быстрее.

### 4. Установи systemd unit

```ini
[Unit]
Description=ZHP2PProxy public RPC and libp2p gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/zhp2pproxy
Environment=HOME=/root
ExecStart=/opt/zhp2pproxy/zhp2pproxy
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

Команды:

```bash
sudo nano /etc/systemd/system/zhp2pproxy.service
sudo systemctl daemon-reload
sudo systemctl enable zhp2pproxy
sudo systemctl restart zhp2pproxy
sudo systemctl status zhp2pproxy --no-pager -l
```

### 5. Проверка

Локально:

```bash
curl -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"2.0","id":1,"method":"getblockcount","params":[]}' \
  http://127.0.0.1:8081/
```

Через главную HTTPS-точку:

```bash
curl -H 'Content-Type: application/json' \
  --data '{"jsonrpc":"2.0","id":1,"method":"getblockcount","params":[]}' \
  https://rpc.zeroscan.st/
```

Для кошельков основной RPC endpoint:

```text
https://rpc.zeroscan.st/
```

Новые публичные RPC endpoints можно добавить в кошелек как резервные, но `https://rpc.zeroscan.st/` остается первой точкой входа.

---

## Конфигурация

Вся конфигурация находится в файле `proxy.json` рядом с бинарником. **Никаких env-переменных.**

### Поля

| Поле | Тип | Описание |
|------|-----|----------|
| `listen` | string | HTTP-адрес прокси, например `:8081` или `127.0.0.1:8081` |
| `p2p_port` | int | TCP-порт libp2p. `0` = случайный порт (удобно для приватных нод) |
| `public_endpoint` | string | Публичный URL этой ноды (если есть). Оставь пустым для приватных нод |
| `rpc_host` | string | Хост локальной ZHCash ноды. По умолчанию `127.0.0.1` |
| `rpc_port` | int | RPC-порт ноды. По умолчанию `3889` |
| `rpc_user` | string | `rpcuser` (если не указан, ищется в `zerohour.conf`) |
| `rpc_pass` | string | `rpcpassword` (если не указан, ищется в `zerohour.conf`) |
| `bootstrap_peers` | []string | Список multiaddrs публичных нод для первичного подключения |
| `rate_limit` | float64 | Запросов в секунду с одного IP |
| `rate_burst` | int | Максимальный burst запросов |
| `api_token` | string | Опциональный Bearer/API token для HTTP доступа. Пусто = auth выключен |
| `allowed_ips` | []string | Опциональный allowlist клиентских IP. Пусто = все IP разрешены |
| `cors_allowed_origins` | []string | Origins для браузерных кошельков. `["*"]` = любой origin |
| `expose_peer_info` | bool | Показывать ли `addresses` и `bootstrap` в `/info`. По умолчанию `false` |
| `p2p_load_balance` | bool | Распределять read-only/cacheable RPC по P2P-пирам. По умолчанию `true` |
| `peer_store_path` | string | Локальный зашифрованный кэш известных P2P peers. По умолчанию `peers.json` рядом с бинарником |
| `browser_ws_enabled` | bool | Включить libp2p WebSocket listen address для browser/PWA клиентов |
| `browser_ws_port` | int | TCP-порт WebSocket транспорта. Если `0`, используется `p2p_port + 1` |
| `browser_webtransport_enabled` | bool | Включить WebTransport listen address |
| `browser_webtransport_port` | int | UDP-порт WebTransport. Если `0`, используется `p2p_port + 2` |
| `browser_webrtc_enabled` | bool | Включить WebRTC Direct listen address |
| `browser_webrtc_port` | int | UDP-порт WebRTC Direct. Если `0`, используется `p2p_port + 3` |
| `relay_enabled` | bool | Включить circuit relay и auto-relay |

### Пример: Публичная bootstrap-нода

```json
{
  "listen": ":8081",
  "p2p_port": 10001,
  "public_endpoint": "http://vps1.example.com:8081",
  "bootstrap_peers": [],
  "rate_limit": 20,
  "rate_burst": 40,
  "api_token": "",
  "allowed_ips": [],
  "cors_allowed_origins": ["*"],
  "expose_peer_info": false,
  "p2p_load_balance": true,
  "peer_store_path": "peers.json",
  "browser_ws_enabled": true,
  "browser_ws_port": 10002,
  "browser_webtransport_enabled": true,
  "browser_webtransport_port": 10003,
  "browser_webrtc_enabled": true,
  "browser_webrtc_port": 10004,
  "relay_enabled": true
}
```

### Пример: Приватная нода за NAT / Windows

```json
{
  "listen": "127.0.0.1:8081",
  "p2p_port": 0,
  "public_endpoint": "",
  "bootstrap_peers": [
    "/ip4/195.200.19.162/tcp/10001/p2p/12D3KooWSkCFqxU32Yojy2FJpUJHxczUVLPzJ5Q41USUnknE4RF9"
  ],
  "rate_limit": 20,
  "rate_burst": 40,
  "api_token": "",
  "allowed_ips": ["127.0.0.1"],
  "cors_allowed_origins": ["http://localhost:5173"],
  "expose_peer_info": false,
  "p2p_load_balance": true,
  "peer_store_path": "peers.json",
  "browser_ws_enabled": true,
  "browser_ws_port": 10002,
  "browser_webtransport_enabled": true,
  "browser_webtransport_port": 10003,
  "browser_webrtc_enabled": true,
  "browser_webrtc_port": 10004,
  "relay_enabled": true
}
```

### Автопоиск zerohour.conf

Если `rpc_user` и `rpc_pass` не указаны в `proxy.json`, программа сама найдёт `zerohour.conf`:

- **Linux**: `~/.zerohour/zerohour.conf`
- **Windows**: `%APPDATA%\ZHCASH\zerohour.conf`
- **macOS**: `~/Library/Application Support/ZHCASH/zerohour.conf`

Если не нашла — попробует подключиться без авторизации. Если и это не сработало — ищет `zerohour.conf` **рядом с исполняемым файлом**.

### Рекомендация txindex

При чтении `zerohour.conf` прокси проверяет настройку `txindex`.

- Если найдено `txindex=1`, нода объявляет поддержку `getrawtransaction`.
- Если `txindex=1` не найдено, прокси пишет рекомендацию включить `txindex=1` для нормальной работы с историческими raw-транзакциями.
- При выключенном `txindex` метод `getrawtransaction` не объявляется в capabilities этой ноды, чтобы другие пиры не отправляли туда заведомо неподдерживаемые запросы.

---

## Запуск

### Публичная нода

```bash
./zhp2pproxy
```

Вывод:
```
[p2p] host created: <peer.ID 12*nE4RF9>
[p2p] listening on: /ip4/195.200.19.162/tcp/10001/p2p/12D3KooWSkCFqxU32Yojy2FJpUJHxczUVLPzJ5Q41USUnknE4RF9
[dht] advertising rendezvous: zhcash-rpc-proxy
[server] listening on :8081
```

Скопируй свой `peer_id` и multiaddr — это твой bootstrap для других нод.

### Приватная нода

1. В `proxy.json` в `bootstrap_peers` вставь multiaddr публичной ноды.
2. Запусти `./zhp2pproxy`.
3. Нода автоматически подключится к bootstrap, найдёт других пиров через DHT и начнёт участвовать в сети.

---

## API

### JSON-RPC

`POST /`

Принимает стандартные JSON-RPC 1.0/2.0 запросы. Поддерживает **batch** (до 100).

```bash
curl -X POST http://127.0.0.1:8081/ \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"1.0","id":1,"method":"getblockcount","params":[]}'
```

Ответ:
```json
{"jsonrpc":"2.0","result":1625137,"id":1}
```

### Информация о ноде

`GET /info`

```json
{
  "peer_id": "12D3KooWSkCFqxU32Yojy2FJpUJHxczUVLPzJ5Q41USUnknE4RF9",
  "relay": true,
  "capabilities": {
    "rpc_available": true,
    "txindex": true,
    "methods": [
      "getblockcount",
      "getnetworkinfo",
      "getrawtransaction",
      "sendrawtransaction"
    ]
  },
  "update": {
    "checked_at": "2026-06-01T12:00:00Z",
    "current_version": "1.2.3",
    "latest_version": "",
    "latest_url": "",
    "update_recommended": false,
    "message": "official Zerohour GitHub has no releases or tags to compare"
  },
  "metrics": {
    "requests_total": 1024,
    "cache_hits": 430,
    "fallback_attempts": 12,
    "fallback_successes": 10,
    "fallback_failures": 2,
    "peer_capability_misses": 1,
    "peer_method_failures": 2,
    "local_rpc_failures": 12,
    "rate_limited_http": 3,
    "unauthorized_http": 1,
    "forbidden_http": 0
  }
}
```

По умолчанию `/info` не раскрывает `addresses` и `bootstrap`, чтобы HTTP-клиенты не видели IP/multiaddr пиров, к которым подключается прокси. Если нужно отладить сеть, временно включи `expose_peer_info: true`.

### Использование из Vite JS / PWA кошелька

Кошелек может использовать `zhp2pproxy` как обычный JSON-RPC endpoint:

```js
export async function rpc(method, params = []) {
  const res = await fetch("https://rpc.example.org/", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: Date.now(),
      method,
      params
    })
  });
  const data = await res.json();
  if (data.error) throw new Error(data.error.message);
  return data.result;
}

const height = await rpc("getblockcount");
```

Для публичного RPC оставь `api_token: ""`, `allowed_ips: []`, `cors_allowed_origins: ["*"]`. Для приватного тестового RPC можно ограничить origins и IP.

Для отказоустойчивости кошелек должен иметь список нескольких HTTP RPC endpoints. Каждый endpoint выглядит как обычный JSON-RPC, а внутри каждая прокси-нода уже работает с P2P-сетью:

```js
const RPC_ENDPOINTS = [
  "https://rpc1.example.org/",
  "https://rpc2.example.org/",
  "https://rpc3.example.org/"
];

let nextEndpoint = 0;

export async function rpc(method, params = []) {
  let lastError;
  for (let i = 0; i < RPC_ENDPOINTS.length; i++) {
    const endpoint = RPC_ENDPOINTS[(nextEndpoint + i) % RPC_ENDPOINTS.length];
    try {
      const res = await fetch(endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ jsonrpc: "2.0", id: Date.now(), method, params })
      });
      const data = await res.json();
      if (data.error) throw new Error(data.error.message);
      nextEndpoint = (nextEndpoint + i + 1) % RPC_ENDPOINTS.length;
      return data.result;
    } catch (err) {
      lastError = err;
    }
  }
  throw lastError;
}
```

Так кошелек не зависит от одной публичной HTTP-ноды. Если один RPC endpoint отвалился, кошелек пробует следующий, а каждая оставшаяся прокси-нода продолжает работать с P2P mesh.

### Browser-libp2p transports

Go-прокси может слушать browser-compatible P2P адреса:

```text
/ip4/0.0.0.0/tcp/10001                       # обычный libp2p TCP для Go-нОД
/ip4/0.0.0.0/tcp/10002/ws                    # WebSocket transport
/ip4/0.0.0.0/udp/10003/quic-v1/webtransport  # WebTransport
/ip4/0.0.0.0/udp/10004/webrtc-direct         # WebRTC Direct
```

Для PWA в продакшене WebSocket адрес обычно публикуется как WSS через reverse proxy:

```text
/dns4/relay.example.org/tcp/443/wss/p2p/PEER_ID
```

WebSocket/WSS дает самый простой browser path. WebTransport и WebRTC Direct добавляют более быстрые/direct варианты, но в реальной сети все равно нужен relay/signaling/bootstrap как надежная база.

### PubSub Broadcast

`POST /pubsub/publish`

Отправляет сообщение в сеть через GossipSub.

```bash
curl -X POST http://127.0.0.1:8081/pubsub/publish \
  -H "Content-Type: application/json" \
  -d '{"type":"tx","data":"deadbeef...","from":"node1","ts":1717260000}'
```

---

## P2P сеть и Bootstrap

### Как ноды находят друг друга

1. **Bootstrap** — при старте нода подключается к адресам из `bootstrap_peers`.
2. **DHT Advertise** — каждая нода объявляет себя под тегом `zhcash-rpc-proxy`.
3. **DHT FindPeers** — каждые 2 минуты нода ищет новых пиров по тому же тегу.
4. **Auto-connect** — найденные пиры автоматически добавляются в peer store.
5. **Encrypted peer cache** — найденные multiaddr сохраняются в `peers.json`, зашифрованный ключом от локального peer ID.
6. **Peer exchange** — подключенные ноды обмениваются `peer_announce` сообщениями через GossipSub.

Если начальная публичная bootstrap-нода упала, уже работавшая нода не теряет всю сеть: она пробует peers из зашифрованного `peers.json`, продолжает DHT-поиск через оставшиеся соединения и учит новые адреса через peer exchange. Отвалившиеся peers получают failure counter и удаляются из cache после повторных ошибок или устаревания.

### Как выбирается пир для RPC fallback

Каждая нода автоматически строит capabilities по своей локальной ZHCash ноде:

1. Проверяет, доступен ли локальный RPC через `getblockcount`.
2. Читает `txindex` из `zerohour.conf`.
3. Формирует список реально доступных методов из whitelist.
4. Отдает список другим пирам через `/zhcash/rpc-capabilities/1.0.0`.

Если включен `p2p_load_balance`, read-only/cacheable методы сначала пробуются на подходящих P2P-пирах, а затем на локальной ноде. Это распределяет нагрузку между прокси-нодами и сохраняет обычную JSON-RPC совместимость для кошельков. Если локальная нода недоступна, fallback также ищет среди подключенных пиров тех, кто объявил нужный метод. Запрос пробуется на нескольких подходящих пирах; пир, который не отвечает или возвращает ошибку метода, временно помечается неудачным для этого метода. Если метод требует `txindex`, запрос уйдет только к пиру, который объявил поддержку этого метода.

### NAT Traversal

| Сценарий | Механизм |
|----------|----------|
| Оба пира публичные | Прямое TCP/QUIC соединение |
| Один за NAT, другой публичный | Прямое исходящее (клиент за NAT инициирует) |
| Оба за NAT (symmetric/strict) | Circuit relay через публичную ноду |
| Оба за NAT (cone/port-restricted) | Hole punching (DCUtR) для прямого канала |

---

## Безопасность

### Whitelist методов

HTTP RPC и P2P stream RPC проходят через один whitelist: `internal/proxy/whitelist.go`.
Все остальные методы возвращают JSON-RPC ошибку `-32601 Method not allowed`.

Разрешены только эти методы:

```text
getblockchaininfo
getblockcount
getblockhash
getblock
getblockheader
getbestblockhash
getdifficulty
getchaintips
getchaintxstats
getnetworkinfo
getrawmempool
getmempoolinfo
getmempoolentry
getmempoolancestors
getmempooldescendants
gettxout
gettxoutsetinfo
verifychain
verifytxoutproof
gettxoutproof
preciousblock
savemempool

createrawtransaction
decoderawtransaction
decodescript
getrawtransaction
sendrawtransaction
testmempoolaccept
combinerawtransaction

validateaddress
verifymessage
gethexaddress
fromhexaddress
signmessagewithprivkey

callcontract
getaccountinfo
getstorage
listcontracts
searchlogs
waitforlogs
gettransactionreceipt

scantxoutset
```

`sendrawtransaction` оставлен намеренно: без него прокси будет только read-only шлюзом и не сможет помогать клиентам распространять уже подписанные транзакции через децентрализованную сеть.

### Запрещенные опасные методы

Эти методы не входят в whitelist и не должны открываться на публичном RPC:

- `pruneblockchain` — удаляет старые блоки с диска локальной ноды.
- `invalidateblock` — помечает блок как невалидный и может вызвать откат активной цепи.
- `reconsiderblock` — снимает invalid-статус с блока и влияет на выбор активной цепи.
- Wallet/private-key методы, включая `dumpprivkey`, `importprivkey`, `sendtoaddress`, `sendmany`.
- Управляющие методы ноды, включая `stop`.

Запрет `pruneblockchain`, `invalidateblock`, `reconsiderblock` закреплен тестом `internal/proxy/whitelist_test.go`.

### Rate limiting

Token bucket per IP: по умолчанию 20 req/s, burst 40. Ограничение применяется ко всем HTTP маршрутам, включая `POST /`, `GET /info` и `POST /pubsub/publish`.

### HTTP auth и IP allowlist

Для полностью публичного RPC оставь `api_token` пустым и `allowed_ips` пустым. Тогда endpoint доступен всем, а защита строится на whitelist, rate limit, capabilities и P2P load balancing.

Для приватного или партнерского RPC можно включить `api_token` и ограничить `allowed_ips`.

- `api_token` принимается через `Authorization: Bearer <token>` или `X-API-Token: <token>`.
- `allowed_ips` проверяет клиентский IP. Если список пустой, IP allowlist выключен.
- При работе за reverse proxy используется первый адрес из `X-Forwarded-For`.
- `cors_allowed_origins` управляет доступом из браузерных PWA. Для публичного кошелька можно использовать `["*"]`.

### Scantxoutset

- `sync.Mutex` — только 1 одновременный запрос (защита от DoS).
- Кэш на 10 минут.

### Fallback

Если локальная нода недоступна, прокси спрашивает подходящих пиров через **зашифрованный libp2p-stream**. Пиры выбираются по capabilities: если пир не объявил нужный метод, запрос туда не отправляется. При ошибке одного пира прокси пробует следующий подходящий пир. `scantxoutset` никогда не fallback'ится.

При `p2p_load_balance: true` этот же механизм используется не только при отказе локальной ноды, но и для распределения read-only/cacheable методов по сети. Write/broadcast методы вроде `sendrawtransaction` не отправляются P2P-first, чтобы не превращать load balancing в транзакционный spam fanout.

### Update notifications

Прокси раз в 6 часов проверяет официальный репозиторий `https://github.com/zerohourcash/zerohourcash`.

- Сначала проверяется GitHub endpoint latest release.
- Если releases отсутствуют, прокси не падает и сообщает, что официальный релиз пока не опубликован.
- Когда релиз появится, прокси сравнит версию локальной ноды из `getnetworkinfo.subversion` с последним релизом и запишет рекомендацию обновления в лог и `/info`.
- Если релиза нет, но есть tags, checker использует последний tag как вспомогательный источник.

### Отчет по безопасности

Текущие защитные меры:

- Единый whitelist применяется на HTTP входе и на P2P stream входе.
- Capabilities не расширяют whitelist: они только сужают список до методов, реально доступных у конкретной ноды.
- HTTP вход защищается per-IP rate limit, опциональным API token и опциональным IP allowlist.
- `/info` по умолчанию не раскрывает IP/multiaddr пиров и bootstrap-адреса.
- `peers.json` хранит найденные P2P адреса в AES-GCM, чтобы локальный файл не раскрывал IP/multiaddr посторонним.
- Wallet-методы и методы управления нодой не опубликованы.
- Административные blockchain-методы `pruneblockchain`, `invalidateblock`, `reconsiderblock` удалены из публичного доступа.
- `sendrawtransaction` разрешен только для трансляции подписанных raw-транзакций; приватные ключи через прокси не передаются.
- Batch ограничен 100 запросами.
- `scantxoutset` сериализован через `sync.Mutex` и кэшируется 10 минут.
- Кэшируются только методы из `CacheableMethods`, опасные write/admin методы туда не входят.

Остаточные риски для публичной ноды:

- `sendrawtransaction` может использоваться для спама уже подписанными транзакциями; ограничивай публичный HTTP через firewall, reverse proxy или rate limit.
- `verifychain`, `scantxoutset`, `searchlogs`, `waitforlogs` могут быть дорогими для ноды при высокой нагрузке.
- `signmessagewithprivkey` подписывает сообщение переданным приватным ключом; прокси не хранит ключи, но клиенту нельзя отправлять приватный ключ через чужой публичный RPC.
- Rate limiting зависит от настройки `rate_limit` и `rate_burst`; для публичного сервера лучше держать консервативные значения.
- P2P fallback доверяет ответу выбранного подключенного пира; capabilities уменьшают число бесполезных запросов, но клиент все равно должен перепроверять критичные данные у нескольких источников или у своей ноды.

---

## Структура кода

Проект небольшой и разделен по ответственности: точка входа собирает конфиг, libp2p, DHT, PubSub и HTTP router; RPC-прокси держит whitelist, кэш, batch-обработку и fallback; P2P слой отвечает за обнаружение и stream RPC.

```
zhp2pproxy/
├── cmd/proxy/main.go          # Точка входа: сборка host, DHT, pubsub, HTTP server
├── internal/
│   ├── config/config.go         # Загрузка proxy.json + авто-поиск zerohour.conf
│   ├── node/rpc.go              # HTTP JSON-RPC клиент к локальной ноде
│   ├── p2p/
│   │   ├── host.go              # Создание libp2p host с NAT traversal
│   │   ├── dht.go               # Kademlia DHT: advertise + find peers
│   │   ├── peerstore.go         # Зашифрованный cache известных P2P peers
│   │   ├── pubsub.go            # GossipSub broadcast
│   │   └── stream.go            # Stream-протокол /zhcash/rpc/1.0.0 + fallback
│   ├── proxy/
│   │   ├── cache.go             # Block-based cache с TTL
│   │   ├── capabilities.go      # Локальные и peer RPC capabilities
│   │   ├── capability_stream.go # libp2p protocol для запроса capabilities
│   │   ├── handler.go           # HTTP handler, batch, scantxoutset queue, fallback
│   │   ├── whitelist.go         # Список разрешённых RPC-методов
│   │   └── whitelist_test.go    # Проверка, что опасные admin-методы запрещены
│   └── server/
│       ├── middleware.go        # CORS, API token, IP allowlist, per-IP rate limit
│       └── server.go            # HTTP router
├── internal/update/checker.go    # Проверка официального GitHub на обновления ZHCash ноды
├── pkg/jsonrpc/types.go         # JSON-RPC Req/Resp/Error
├── proxy.json                   # Конфиг
├── build.sh                     # Скрипт сборки
└── README.md                    # Этот файл
```

### Роли основных модулей

| Путь | Ответственность |
|------|-----------------|
| `cmd/proxy/main.go` | Инициализация конфигурации, локального RPC-клиента, libp2p host, DHT, PubSub и HTTP сервера |
| `internal/config/config.go` | Загрузка `proxy.json`, дефолты, поиск RPC credentials в `zerohour.conf` |
| `internal/node/rpc.go` | Простой JSON-RPC клиент к локальной ZHCash ноде |
| `internal/proxy/handler.go` | Прием HTTP JSON-RPC, batch до 100 запросов, whitelist, кэш, `scantxoutset`, P2P fallback |
| `internal/proxy/capabilities.go` | Определение локальных методов и registry возможностей подключенных пиров |
| `internal/proxy/capability_stream.go` | libp2p endpoint `/zhcash/rpc-capabilities/1.0.0` для обмена возможностями |
| `internal/proxy/whitelist.go` | Единственный список публично разрешенных RPC-методов |
| `internal/proxy/cache.go` | In-memory кэш результатов RPC с TTL |
| `internal/update/checker.go` | Проверка официального GitHub на releases/tags и рекомендация обновления ноды |
| `internal/p2p/host.go` | Создание libp2p host с relay/NAT возможностями |
| `internal/p2p/dht.go` | Kademlia advertise/find peers через rendezvous `zhcash-rpc-proxy` |
| `internal/p2p/peerstore.go` | Зашифрованный живой cache известных peers с TTL и failure eviction |
| `internal/p2p/pubsub.go` | GossipSub broadcast сообщений |
| `internal/p2p/stream.go` | JSON-RPC поверх libp2p stream `/zhcash/rpc/1.0.0` |
| `internal/server/middleware.go` | CORS, HTTP auth, IP allowlist и token bucket rate limit |
| `internal/server/server.go` | HTTP маршруты `/`, `/info`, `/pubsub/publish` |
| `pkg/jsonrpc/types.go` | Общие структуры JSON-RPC запросов, ответов и ошибок |

---

## Примеры сценариев

### Сценарий 1: Публичный RPC-шлюз

У тебя есть VPS с публичным IP. Ты запускаешь `zhp2pproxy` — теперь любой может обращаться к твоей ноде через HTTP JSON-RPC, а другие ноды используют тебя как bootstrap и relay.

### Сценарий 2: Приватная нода за роутером

Твой компьютер дома за NAT. Ты указываешь в `bootstrap_peers` адрес друга с VPS. Твоя нода подключается к нему, находит через DHT ещё 10 пиров, и теперь может:
- Отправлять `sendrawtransaction` через fallback на публичные ноды.
- Получать broadcast о новых блоках через GossipSub.
- Обмениваться данными с другими приватными нодами через hole punching.

### Сценарий 3: Mesh без центра

5 друзей запускают ноды. Каждый знает хотя бы 1 другого. Через DHT и gossip они находят всех остальных. Сеть работает даже если 2 из 5 нод выключены — остальные держат связь через оставшиеся.

---

## Troubleshooting

### Нода не находит пиров

- Проверь `bootstrap_peers` — адреса должны быть в формате multiaddr: `/ip4/IP/tcp/PORT/p2p/PEER_ID`
- Убедись, что публичная нода запущена и порт `p2p_port` открыт в фаерволе.
- Проверь лог: `[dht] advertising rendezvous` и `[dht] connected to peer`.

### RPC возвращает "Method not allowed"

- Метод не в whitelist. Смотри `internal/proxy/whitelist.go`.

### Ошибка "cannot find valid RPC credentials"

- `zerohour.conf` не найден. Положи его рядом с бинарником или укажи `rpc_user`/`rpc_pass` в `proxy.json`.

### Приватная нода не видит входящих

- Это нормально. Приватная нода работает через исходящие соединения и relay. Убедись, что `relay_enabled: true`.

---

## Лицензия

MIT / Public Domain — используй на свой страх и риск.
