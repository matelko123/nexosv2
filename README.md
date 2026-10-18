# NexOS

Zarządzanie kilkoma homelabami z jednej aplikacji webowej. **Hub** (UI + API) działa na serwerze A, **agent** na każdym zarządzanym serwerze. Komunikacja hub → agent idzie wyłącznie przez tunel WireGuard i gRPC z wzajemnym TLS.

```
Przeglądarka ──HTTPS──► HUB (serwer A) ──gRPC/mTLS po wg0──► AGENT A (10.66.0.1:7443)
                                                        └──► AGENT B (10.66.0.2:7443)
```

## Stan: Fazy 1–5

- PKI z własnym CA, rejestracja agenta jednorazowym pakietem, automatyczna rotacja certyfikatów (30 dni, odnawiane 10 dni przed końcem), pinning certyfikatów agentów w hubie
- Logowanie: argon2id + obowiązkowe TOTP (z ochroną przed ponownym użyciem kodu), sesje HttpOnly/SameSite=Strict, CSRF, rate limiting, RBAC, dziennik audytu
- Metryki hosta na żywo (CPU per rdzeń, RAM/swap, dyski, I/O, ruch per interfejs, temperatury, load) + historia w SQLite (domyślnie 24 h)
- UI: lista hostów, szczegóły hosta z wykresami, dodawanie hostów, audyt

- Kontenery: lista, akcje, logi na żywo, statystyki, terminal (exec), zdarzenia Dockera
- Obrazy (pull z postępem, usuwanie, prune), sieci (tworzenie, podłączanie kontenerów), wolumeny
- Stacki compose: edytor compose.yaml i .env z walidacją, up/down/pull/restart z podglądem na żywo; pozostałe projekty compose na hoście są wykrywane automatycznie

- Obserwowalność: historia metryk hosta do 90 dni (5 s → 1 min → 5 min), historia zasobów kontenerów, alerty (host offline, CPU, RAM, dysk, temperatura, problemy kontenerów, aktualizacje obrazów) z powiadomieniami ntfy / Discord / Telegram / webhook (HMAC) / e-mail, wykrywanie nowych wersji obrazów w rejestrze

- Funkcje hosta (opt-in per agent): procesy z kill, usługi systemd z dziennikiem journald, podgląd aktualizacji pakietów apt i wymaganego restartu
- Użytkownicy: zaproszenia jednorazowym linkiem, role viewer/operator/admin, reset dostępu, zmiana hasła, sesje, passkeys (WebAuthn) jako alternatywa dla TOTP

## Model bezpieczeństwa

| Warstwa | Co chroni |
|---|---|
| WireGuard | Agent nie jest wystawiony do internetu. B nie otwiera żadnych portów (to on łączy się z A). Port 51821, żeby nie kolidować z wg-easy na 51820. |
| nftables (`deploy/firewall`) | Port agenta przyjmuje tylko adres huba na `wg0`; enrollment tylko z `wg0`; UI tylko z LAN/WG. |
| mTLS (TLS 1.3) | Agent przyjmuje wyłącznie certyfikat `hub.nexos` z CA huba. Hub przyjmuje wyłącznie certyfikat, który sam wydał temu agentowi i którego nie unieważnił. |
| Enrollment | Pakiet jednorazowy, ważny 15 min, zawiera odcisk CA (agent nie zaufa innemu serwerowi). Klucz prywatny agenta nigdy nie opuszcza serwera. |
| UI | argon2id, TOTP, CSRF, CSP, HSTS. Certyfikat UI pochodzi z **osobnego** CA — zaufanie mu w przeglądarce nie daje nic względem PKI agentów. |

Agent ma dostęp do `docker.sock`, czyli jest równoważny rootowi na hoście — dlatego jest osiągalny tylko przez WG + mTLS i tylko dla huba.

## Wdrożenie

### 1. WireGuard (na obu serwerach, poza Dockerem)

```sh
apt install wireguard
wg genkey | tee /etc/wireguard/a.key | wg pubkey > /etc/wireguard/a.pub   # na A (analogicznie b.* na B)
wg genpsk > ab.psk                                                         # jeden wspólny PSK
```

Uzupełnij `deploy/wireguard/wg0-a.conf.example` (A) i `wg0-b.conf.example` (B), zapisz jako `/etc/wireguard/wg0.conf`, przekieruj UDP 51821 na routerze lokalizacji A na serwer A, potem:

```sh
systemctl enable --now wg-quick@wg0
wg show          # powinien być "latest handshake"
ping 10.66.0.2   # z A
```

### 2. Firewall

```sh
nft -f deploy/firewall/nftables-hub.nft     # na A (popraw LAN)
nft -f deploy/firewall/nftables-agent.nft   # na B
```

### 3. Hub (serwer A)

```sh
cd deploy/hub
docker compose up -d --build
docker compose logs nexos-hub | grep setup_token
```

Otwórz `https://<IP-serwera-A>:8443`, podaj token, utwórz konto i zeskanuj kod TOTP. Certyfikat UI jest podpisany przez CA z wolumenu (`/data/ui-pki/ca.crt`) — możesz go zaimportować do przeglądarki, albo postawić hub za reverse proxy (`NEXOS_UI_TLS=off`, `NEXOS_TRUSTED_PROXIES`). Dodaj adres/nazwę, pod którą otwierasz UI, do `NEXOS_UI_HOSTS`.

### 4. Agenci

W UI: **Dodaj host** → nazwa + adres agenta (`10.66.0.1:7443` dla A, `10.66.0.2:7443` dla B). Skopiuj pokazany pakiet do `deploy/agent/.env` na danym serwerze:

```sh
NEXOS_LISTEN=10.66.0.2:7443
NEXOS_ENROLL=nxe1_...
```

```sh
cd deploy/agent && docker compose up -d --build
```

Po rejestracji usuń `NEXOS_ENROLL` z `.env`. Agent odmówi startu na `0.0.0.0` (wymaga konkretnego adresu WG).

## Konfiguracja

| Zmienna | Domyślnie | Opis |
|---|---|---|
| **Hub** | | |
| `NEXOS_UI_LISTEN` | `:8443` | Adres UI |
| `NEXOS_UI_TLS` | `auto` | `auto` (własny cert) lub `off` (za proxy HTTPS) |
| `NEXOS_UI_HOSTS` | | Dodatkowe nazwy/IP w certyfikacie UI |
| `NEXOS_ENROLL_LISTEN` | (wyłączone) | Adres endpointu rejestracji, np. `10.66.0.1:7444` |
| `NEXOS_ENROLL_ADVERTISE` | = listen | Adres podawany agentom w pakiecie |
| `NEXOS_METRICS_INTERVAL` | `5s` | Częstotliwość metryk |
| `NEXOS_METRICS_RETENTION` | `24h` | Retencja surowych próbek (co 5 s) |
| `NEXOS_RETENTION_1M` / `NEXOS_RETENTION_5M` | `168h` / `2160h` | Retencja średnich minutowych / 5-minutowych |
| `NEXOS_RETENTION_CONTAINERS` | `168h` | Retencja historii zasobów kontenerów |
| `NEXOS_RETENTION_ALERTS` | `2160h` | Retencja rozwiązanych alertów |
| `NEXOS_UPDATE_CHECK_INTERVAL` | `6h` | Co ile sprawdzać nowe wersje obrazów (`0` wyłącza) |
| `NEXOS_TRUSTED_PROXIES` | | CIDR-y proxy, którym ufamy w `X-Forwarded-For` |
| `NEXOS_SETUP_TOKEN` | losowy | Token pierwszej konfiguracji |
| `NEXOS_WEBAUTHN_ORIGINS` | | Dodatkowe originy dla passkeys (np. serwer deweloperski) |
| **Agent** | | |
| `NEXOS_LISTEN` | wymagane | Adres WG agenta, np. `10.66.0.2:7443` |
| `NEXOS_ENROLL` | | Pakiet rejestracyjny (tylko pierwsze uruchomienie) |
| `NEXOS_HOSTNAME` | hostname | Nazwa pokazywana w UI |
| `NEXOS_STACKS_DIR` | `/opt/nexos/stacks` | Katalog stacków zarządzanych przez NexOS (montowany pod tą samą ścieżką co na hoście) |
| `NEXOS_HOST_FEATURES` | | Funkcje hosta: `processes,systemd,packages` (patrz `deploy/agent/compose.host-features.yaml`) |

## Rozwój

```sh
make proto    # buf generate (wymaga buf, protoc-gen-go, protoc-gen-go-grpc)
make test     # go vet + go test + typecheck frontendu
cd web && npm run dev   # UI na :5173, proxy do huba na https://localhost:8443
```

Struktura: `proto/` kontrakt gRPC · `internal/pki` CA i konfiguracje TLS · `internal/enroll` rejestracja · `agent/` agent · `hub/` hub (api, auth, agents, store, certs) · `web/` React UI · `deploy/` compose, WireGuard, nftables.
