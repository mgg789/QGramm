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
python3 tools/browser-webrtc/run-docker.py
```

Docker runner использует собственный временный контейнер и образ, не публикует порты на хост и удаляет их в `finally`. Node/Go сервер, браузер и тестовая страница находятся внутри этого контейнера. Go бинарный сервер, браузерный профиль и серверные данные удаляются после прогона. Зависимости/npm cache/build cache могут сохраняться как обычный кеш сборки; секреты туда не попадают.

## Что проверяет страница

Два независимых native `RTCPeerConnection` передают generated audio через `AudioContext` oscillator и generated video через `canvas.captureStream`. Камера и микрофон не запрашиваются. Это реальные браузерные media tracks, кодирование, RTP, DTLS-SRTP, декодирование и video frame callbacks; API и media не заменяются заглушками.

Offer/answer и настоящие gathered ICE candidates проходят действительный Go calls API через HPKE библиотеку `@hpke/core` (X25519/HKDF-SHA256/AES128-GCM). Входной и выходной AAD соответствует `cryptoenc.Binding`; после recipient decrypt сравнивается полный SDP и fingerprint. Страница отвергает открытые `sdp`/`candidate` поля в HTTP signal/event JSON.

Положительные audio/video сценарии требуют connected transports у обоих peers, более 1000 полученных audio samples с положительной audio energy, а video сценарий — минимум пять decoded и rendered кадров, `videoWidth=160` у обоих peers. Отрицательный сценарий передаёт через тот же HPKE/API SDP с подменённым SHA-256 fingerprint: caller должен перейти в `connectionState=failed` и получить ноль media bytes.

Ограниченные test-only process flags разрешают autoplay и loopback ICE, отключают mDNS masking для воспроизводимого локального транспорта. Это настройки только одноразового браузерного процесса; host/network policy и пользовательские настройки не меняются.

## Наблюдения и границы

Штатный in-app/extension browser runtime был проверен первым согласно browser skill: `getDefault()` вернул `No browser is available`; после bootstrap troubleshooting `browsers.list()` вернул `[]`. Поэтому применён разрешённый fallback Playwright с отдельным native браузером.

На macOS Google Chrome 154.0.8037.95 HPKE offer/answer/ICE обмен через настоящий сервер прошёл, но direct media transport оставался ICE checking: оба peers отправляли STUN requests, не получали requests/responses и имели нулевые audio/video счётчики. Это не успешная native media проверка. Host permission/firewall/network settings не менялись.

Финальный Linux Docker прогон 2026-10-04: native Chromium 152.0.7977.82, Linux arm64, Node 24.18.1. Команда `python3 tools/browser-webrtc/run-docker.py` собрала реальный calls artifact и завершилась **exit 1**: полная audio/video acceptance не достигнута. Runner сохраняет независимые результаты и не превращает частичный успех в PASS.

| Проверка | Фактический результат |
| --- | --- |
| HPKE offer/answer/ICE, полный SDP и fingerprint после recipient decrypt | PASS в обоих положительных сценариях |
| Native video direct | PASS: peers connected; decoded 244/242 кадров, rendered 204/132, ширина 160 |
| Подмена SHA-256 DTLS fingerprint через HPKE/API | PASS: caller `connectionState=failed`, `mediaBytesReceived=0` |
| Native audio direct | FAIL acceptance: 613 RTP packets и 49210/49240 bytes получены, но AudioWorklet PCM energy=0 при 531200 samples на каждом peer |
| Audio в video-сценарии | FAIL acceptance: 610 RTP packets на peer, AudioWorklet PCM energy=0 при 537600 samples |

Для отделения молчащего генератора от проблемы на получателе измерены browser `media-source` stats: в audio-сценарии sender audioLevel≈0.19999 и totalAudioEnergy≈0.48794/0.48234; в video-сценарии totalAudioEnergy≈0.49034/0.48474. Поэтому генератор не молчал. Receiver `inbound-rtp.totalSamplesReceived` и `totalAudioEnergy` оставались нулевыми. Причина отсутствия ненулевого PCM в этом hardware-free Chromium receiver стенде не установлена; ни decoded browser audio, ни дефект QGramm core этим прогоном не подтверждены. Новые настройки host/audio/network для обхода ограничения не применялись.

`node --check page.js`, `node --check run.mjs`, `node --check pcm.js` из `tools/browser-webrtc` — PASS. Одноразовый Docker контейнер/образ, backend-процесс и данные очищены после финального прогона; локальный `node_modules` удалён, lockfile сохранён.

Этот стенд не проверяет камеру/микрофон, слышимое воспроизведение динамиками, рабочий UI, Safari/Firefox, публичный NAT или MLS authentication of fingerprints. TURN отдельно проверен Pion стендом из [webrtc-verification.md](webrtc-verification.md); browser runner пока проверяет direct media.

Источники: [HPKE JS](https://github.com/dajiaji/hpke-js), [Chromium loopback peer connection switch](https://chromium.googlesource.com/chromium/chromium/+/HEAD/content/public/common/content_switches.cc).
