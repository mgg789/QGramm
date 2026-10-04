# Native browser WebRTC acceptance

`tools/browser-webrtc` — отдельная временная страница и интеграционный runner, а не поставляемый QGramm UI/SDK. Зависимости зафиксированы в локальном `package-lock.json`: `@hpke/core` 1.9.0, Playwright Core 1.63.0, jose 6.2.12 и esbuild 0.28.2. Корневые Go/npm зависимости не меняются.

## Запуск

На macOS с установленным Google Chrome:

```sh
cd tools/browser-webrtc
npm ci --ignore-scripts
npm run check
```

Другой установленный Chromium/Chrome можно выбрать через `QGRAMM_BROWSER_EXECUTABLE`. Go должен быть доступен в PATH. Runner строит настоящий calls artifact штатным `cmd/qgramm-build`, запускает сервер и страницу на случайных loopback портах, генерирует одноразовые Ed25519 JWT и серверные ключи. Management/master/HPKE секреты остаются в backend-процессе; страница получает только короткие токены своих тестовых устройств и генерирует собственные X25519 ключи в браузере. Пользовательский профиль Chrome не используется.

Для отдельного Linux Chromium контейнера из корня репозитория:

```sh
python3 tools/browser-webrtc/run-docker.py --output docs/benchmarks/browser-webrtc-final.json
```

Docker runner использует собственный временный образ, browser/backend контейнер, отдельный coturn контейнер и Docker сеть. Он не публикует порты на хост и удаляет все эти ресурсы в `finally`. Coturn работает с одноразовым REST authentication secret; тот же secret передаётся серверу через временный env-файл. Браузер получает credentials только через настоящий `/v1/calls/turn` для своих устройств. Node/Go сервер, браузер и тестовая страница находятся внутри browser контейнера. Go бинарный сервер, браузерный профиль и серверные данные удаляются после прогона. Зависимости/npm cache/build cache могут сохраняться как обычный кеш сборки; секреты туда не попадают.

## Что проверяет страница

Два независимых native `RTCPeerConnection` передают generated audio через `AudioContext` oscillator и generated video через `canvas.captureStream`. Камера и микрофон не запрашиваются. Это реальные браузерные media tracks, кодирование, RTP, DTLS-SRTP, декодирование и video frame callbacks; API и media не заменяются заглушками.

Offer/answer и настоящие gathered ICE candidates проходят действительный Go calls API через HPKE библиотеку `@hpke/core` (X25519/HKDF-SHA256/AES128-GCM). Входной и выходной AAD соответствует `cryptoenc.Binding`; после recipient decrypt сравнивается полный SDP и fingerprint. Страница отвергает открытые `sdp`/`candidate` поля в HTTP signal/event JSON.

Положительные audio/video сценарии требуют connected transports у обоих peers, более 1000 полученных audio samples с положительной audio energy, а video сценарий — минимум пять decoded и rendered кадров, `videoWidth=160` у обоих peers. Отрицательный сценарий передаёт через тот же HPKE/API SDP с подменённым SHA-256 fingerprint: caller должен перейти в `connectionState=failed` и получить ноль media bytes.

Audio receiver подключает remote track к настоящему играющему `HTMLAudioElement` и параллельно к `MediaStreamAudioSourceNode → AudioWorklet`. В Chromium этого стенда один AudioWorklet с silent sink получал нулевой PCM, хотя RTP и sender energy были положительными; добавление `audio.play()` включило декодирование и позволило измерить ненулевой PCM. `AudioContext` сохраняет silent sink: воспроизведение динамиками этим не подтверждается.

TURN сценарии используют `iceTransportPolicy="relay"`. Acceptance дополнительно требует `transport.selectedCandidatePairId`, успешную selected pair и `candidateType="relay"` у local и remote candidates обоих peers. RTP bytes остаются дополнительным условием; они не заменяют decoded PCM energy.

Ограниченные test-only process flags разрешают autoplay и loopback ICE, отключают mDNS masking для воспроизводимого локального транспорта. Это настройки только одноразового браузерного процесса; host/network policy и пользовательские настройки не меняются.

## Наблюдения и границы

Штатный in-app/extension browser runtime был проверен первым согласно browser skill: `getDefault()` вернул `No browser is available`; после bootstrap troubleshooting `browsers.list()` вернул `[]`. Поэтому применён разрешённый fallback Playwright с отдельным native браузером.

На macOS Google Chrome 154.0.8037.95 HPKE offer/answer/ICE обмен через настоящий сервер прошёл, но direct media transport оставался ICE checking: оба peers отправляли STUN requests, не получали requests/responses и имели нулевые audio/video счётчики. Это не успешная native media проверка. Host permission/firewall/network settings не менялись.

Финальный Linux Docker прогон записан в [benchmarks/browser-webrtc-final.json](benchmarks/browser-webrtc-final.json). Артефакт содержит отдельные результаты каждого сценария, browser/runtime, commit, dirty-tree provenance, время UTC и код завершения. 2026-10-04, Chromium 152.0.7977.82, Linux arm64: команда завершилась **exit 0**. Это запуск на commit `0b4dd0bd3b869f0f88cfa8be4dd0fa8f03847c1f` с текущими незакоммиченными изменениями fixture; `workingTreeDirty=true` записан явно.

| Сценарий | Фактический результат обоих peers |
| --- | --- |
| Native audio direct | PASS: PCM samples 12800/12800, energy 213.82/206.26; selected pair host/host |
| Native audio/video direct | PASS: PCM samples 12800/19200, energy 215.16/318.04; decoded video 9/9, rendered 6/6, width 160 |
| Native audio TURN | PASS: PCM samples 12800/19200, energy 158.34/176.84; selected pair relay/relay succeeded |
| Native audio/video TURN | PASS: PCM samples 12800/19200, energy 167.07/165.62; decoded video 8/8, rendered 6/6, width 160; selected pair relay/relay succeeded |
| Подмена SHA-256 DTLS fingerprint через HPKE/API | PASS: caller connectionState=failed, mediaBytesReceived=0 |

Все четыре положительных сценария подтвердили HPKE fingerprint binding и ненулевой decoded PCM. После прогона отдельными `docker ps -a`, `docker network ls`, `docker images` фильтрами проверено отсутствие собственных контейнеров, сети и образа запуска `qgramm-browser-6564452401`.

`node --check tools/browser-webrtc/page.js`, `node --check tools/browser-webrtc/run.mjs`, `node --check tools/browser-webrtc/pcm.js` и `python3 -m py_compile tools/browser-webrtc/run-docker.py` — PASS.

Этот стенд проверяет локальную native Chromium media обработку и локальный coturn relay. Он не проверяет камеру/микрофон, слышимое воспроизведение динамиками, рабочий UI, Safari/Firefox, публичный NAT или MLS authentication of fingerprints. Независимый Pion стенд описан в [webrtc-verification.md](webrtc-verification.md).

Источники: [HPKE JS](https://github.com/dajiaji/hpke-js), [Chromium loopback peer connection switch](https://chromium.googlesource.com/chromium/chromium/+/HEAD/content/public/common/content_switches.cc).
