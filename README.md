# NexOS

ZarzÄdzanie kilkoma homelabami z jednej aplikacji webowej. **Hub** (UI + API) dziaÅa na serwerze A, **agent** na kaÅ¼dym zarzÄdzanym serwerze. Komunikacja hub â agent idzie wyÅÄcznie przez tunel WireGuard i gRPC z wzajemnym TLS.

```
PrzeglÄdarka ââHTTPSâââº HUB (serwer A) ââgRPC/mTLS po wg0âââº AGENT A (10.66.0.1:7443)
                                                        ââââº AGENT B (10.66.0.2:7443)
```

## Stan: Fazy 1â5

- PKI z wÅasnym CA, rejestracja agenta jednorazowym pakietem, automatyczna rotacja certyfikatÃ³w (30 dni, odnawiane 10 dni przed koÅcem), pinning certyfikatÃ³w agentÃ³w w hubie
- Logowanie: argon2id + obowiÄzkowe TOTP (z ochronÄ przed ponownym uÅ¼yciem kodu), sesje HttpOnly/SameSite=Strict, CSRF, rate limiting, RBAC, dziennik audytu
- Metryki hosta na Å¼ywo (CPU per rdzeÅ, RAM/swap, dyski, I/O, ruch per interfejs, temperatury, load) + historia w SQLite (domyÅlnie 24 h)
- UI: lista hostÃ³w, szczegÃ³Åy hosta z wykresami, dodawanie hostÃ³w, audyt

- Kontenery: lista, akcje, logi na Å¼ywo, statystyki, terminal (exec), zdarzenia Dockera
- Obrazy (pull z postÄpem, usuwanie, prune), sieci (tworzenie, podÅÄczanie kontenerÃ³w), wolumeny
- Stacki compose: edytor compose.yaml i .env z walidacjÄ, up/down/pull/restart z podglÄdem na Å¼ywo; pozostaÅe projekty compose na hoÅcie sÄ wykrywane automatycznie

- ObserwowalnoÅÄ: historia metryk hosta do 90 dni (5 s â 1 min â 5 min), historia zasobÃ³w kontenerÃ³w, alerty (host offline, CPU, RAM, dysk, temperatura, problemy kontenerÃ³w, aktualizacje obrazÃ³w) z powiadomieniami ntfy / Discord / Telegram / webhook (HMAC) / e-mail, wykrywanie nowych wersji obrazÃ³w w rejestrze

- Funkcje hosta (opt-in per agent): procesy z kill, usÅugi systemd z dziennikiem journald, podglÄd aktualizacji pakietÃ³w apt i wymaganego restartu
- UÅ¼ytkownicy: zaproszenia jednorazowym linkiem, role viewer/operator/admin, reset dostÄpu, zmiana hasÅa, sesje, passkeys (WebAuthn) jako alternatywa dla TOTP

## Model bezpieczeÅstwa

| Warstwa | Co chroni |
|---|---|
| WireGuard | Agent nie jest wystawiony do internetu. B nie otwiera Å¼adnych portÃ³w (to on ÅÄczy siÄ z A). Port 51821, Å¼eby nie kolidowaÄ z wg-easy na 51820. |
| nftables (`deploy/firewall`) | Port agenta przyjmuje tylko adres huba na `wg0`; enrollment tylko z `wg0`; UI tylko z LAN/WG. |
| mTLS (TLS 1.3) | Agent przyjmuje wyÅÄcznie certyfikat `hub.nexos` z CA huba. Hub przyjmuje wyÅÄcznie certyfikat, ktÃ³ry sam wydaÅ temu agentowi i ktÃ³rego nie uniewaÅ¼niÅ. |
| Enrollment | Pakiet jednorazowy, waÅ¼ny 15 min, zawiera odcisk CA (agent nie zaufa innemu serwerowi). Klucz prywatny agenta nigdy nie opuszcza serwera. |
| UI | argon2id, TOTP, CSRF, CSP, HSTS. Certyfikat UI pochodzi z **osobnego** CA â zaufanie mu w przeglÄdarce nie daje nic wzglÄdem PKI agentÃ³w. |

Agent ma dostÄp do `docker.sock`, czyli jest rÃ³wnowaÅ¼ny rootowi na hoÅcie â dlatego jest osiÄgalny tylko przez WG + mTLS i tylko dla huba.

## WdroÅ¼enie

### 1. WireGuard (na obu serwerach, poza Dockerem)

```sh
apt install wireguard
wg genkey | tee /etc/wireguard/a.key | wg pubkey > /etc/wireguard/a.pub   # na A (analogicznie b.* na B)
wg genpsk > ab.psk                                                         # jeden wspÃ³lny PSK
```

UzupeÅnij `deploy/wireguard/wg0-a.conf.example` (A) i `wg0-b.conf.example` (B), zapisz jako `/etc/wireguard/wg0.conf`, przekieruj UDP 51821 na routerze lokalizacji A na serwer A, potem:

```sh
systemctl enable --now wg-quick@wg0
wg show          # powinien byÄ "latest handshake"
ping 10.66.0.2   # z A
```

### 2. Firewall

```sh
nft -f deploy/firewall/nftables-hub.nft     # na A (popraw LAN)
nft -f deploy/firewall/nftables-agent.nft   # na B
```

### 3. Hub (serwer A)

```sh
cd deploy/hub   # obrazy: ghcr.io/matelko123/nexosv2-* (lokalnie: make docker)
docker compose up -d
docker compose logs nexos-hub | grep setup_token
```

OtwÃ³rz `https://<IP-serwera-A>:8443`, podaj token, utwÃ³rz konto i zeskanuj kod TOTP. Certyfikat UI jest podpisany przez CA z wolumenu (`/data/ui-pki/ca.crt`) â moÅ¼esz go zaimportowaÄ do przeglÄdarki, albo postawiÄ hub za reverse proxy (`NEXOS_UI_TLS=off`, `NEXOS_TRUSTED_PROXIES`). Dodaj adres/nazwÄ, pod ktÃ³rÄ otwierasz UI, do `NEXOS_UI_HOSTS`.

### 4. Agenci

W UI: **Dodaj host** â nazwa + adres agenta (`10.66.0.1:7443` dla A, `10.66.0.2:7443` dla B). Skopiuj pokazany pakiet do `deploy/agent/.env` na danym serwerze:

```sh
NEXOS_LISTEN=10.66.0.2:7443
NEXOS_ENROLL=nxe1_...
```

```sh
cd deploy/agent && docker compose up -d
```

Po rejestracji usuÅ `NEXOS_ENROLL` z `.env`. Agent odmÃ³wi startu na `0.0.0.0` (wymaga konkretnego adresu WG).

## Aktualizacje NexOS

Wydanie powstaje po wypchniÄciu tagu `vX.Y.Z` (`make release VERSION=v1.2.0`): GitHub Actions buduje obrazy `ghcr.io/matelko123/nexosv2-hub` i `-agent` (amd64 + arm64) i publikuje Release.

Hub co 6 h sprawdza najnowsze wydanie i pokazuje administratorom baner z opisem zmian. Aktualizacja huba lub agenta odbywa siÄ **wyÅÄcznie po zatwierdzeniu w UI** (Aktualizacje): agent pobiera obraz z oficjalnego repozytorium (inne sÄ odrzucane), oznacza go nazwÄ z pliku compose i uruchamia krÃ³tkotrwaÅy kontener pomocniczy, ktÃ³ry wykonuje `docker compose up -d` dla danej usÅugi. Hub nie ma dostÄpu do Dockera, wiÄc jego aktualizacjÄ wykonuje agent z tego samego hosta. Wymagane: hub i agent uruchomione przez docker compose z etykietami `nexos.role` (jak w `deploy/`).

Prywatne repozytorium: ustaw `NEXOS_UPDATE_GITHUB_TOKEN` w hubie (token klasyczny z zakresami `repo` i `read:packages`) — sÅuÅ¼y do odczytu wydaÅ i jest przekazywany agentom do pobrania obrazÃ³w z ghcr.io.

## Konfiguracja

| Zmienna | DomyÅlnie | Opis |
|---|---|---|
| **Hub** | | |
| `NEXOS_UI_LISTEN` | `:8443` | Adres UI |
| `NEXOS_UI_TLS` | `auto` | `auto` (wÅasny cert) lub `off` (za proxy HTTPS) |
| `NEXOS_UI_HOSTS` | | Dodatkowe nazwy/IP w certyfikacie UI |
| `NEXOS_ENROLL_LISTEN` | (wyÅÄczone) | Adres endpointu rejestracji, np. `10.66.0.1:7444` |
| `NEXOS_ENROLL_ADVERTISE` | = listen | Adres podawany agentom w pakiecie |
| `NEXOS_METRICS_INTERVAL` | `5s` | CzÄstotliwoÅÄ metryk |
| `NEXOS_METRICS_RETENTION` | `24h` | Retencja surowych prÃ³bek (co 5 s) |
| `NEXOS_RETENTION_1M` / `NEXOS_RETENTION_5M` | `168h` / `2160h` | Retencja Årednich minutowych / 5-minutowych |
| `NEXOS_RETENTION_CONTAINERS` | `168h` | Retencja historii zasobÃ³w kontenerÃ³w |
| `NEXOS_RETENTION_ALERTS` | `2160h` | Retencja rozwiÄzanych alertÃ³w |
| `NEXOS_UPDATE_CHECK_INTERVAL` | `6h` | Co ile sprawdzaÄ nowe wersje obrazÃ³w (`0` wyÅÄcza) |
| `NEXOS_TRUSTED_PROXIES` | | CIDR-y proxy, ktÃ³rym ufamy w `X-Forwarded-For` |
| `NEXOS_SETUP_TOKEN` | losowy | Token pierwszej konfiguracji |
| `NEXOS_WEBAUTHN_ORIGINS` | | Dodatkowe originy dla passkeys (np. serwer deweloperski) |
| **Agent** | | |
| `NEXOS_LISTEN` | wymagane | Adres WG agenta, np. `10.66.0.2:7443` |
| `NEXOS_ENROLL` | | Pakiet rejestracyjny (tylko pierwsze uruchomienie) |
| `NEXOS_HOSTNAME` | hostname | Nazwa pokazywana w UI |
| `NEXOS_STACKS_DIR` | `/opt/nexos/stacks` | Katalog stackÃ³w zarzÄdzanych przez NexOS (montowany pod tÄ samÄ ÅcieÅ¼kÄ co na hoÅcie) |
| `NEXOS_HOST_FEATURES` | | Funkcje hosta: `processes,systemd,packages` (patrz `deploy/agent/compose.host-features.yaml`) |

## RozwÃ³j

```sh
make proto    # buf generate (wymaga buf, protoc-gen-go, protoc-gen-go-grpc)
make test     # go vet + go test + typecheck frontendu
cd web && npm run dev   # UI na :5173, proxy do huba na https://localhost:8443
```

Struktura: `proto/` kontrakt gRPC Â· `internal/pki` CA i konfiguracje TLS Â· `internal/enroll` rejestracja Â· `agent/` agent Â· `hub/` hub (api, auth, agents, store, certs) Â· `web/` React UI Â· `deploy/` compose, WireGuard, nftables.
