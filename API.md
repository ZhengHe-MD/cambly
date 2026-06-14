# Cambly Web API — reverse-engineered reference

Notes from reverse-engineering the Cambly **student** web app (`www.cambly.com`)
via the Chrome DevTools Protocol. This documents only what the CLI uses. Values
shown are illustrative.

## Base & auth

- **Base URL:** `https://www.cambly.com`
- **Auth:** a signed Flask **`session` cookie**. Its payload decodes to
  `{"_permanent": true, "token": "<sessionToken>"}` — the server signs it, so it
  can't be forged client-side, but once issued it works from any HTTP client
  (cookie → `Cookie: session=<value>`). Valid ~30 days.
- **CSRF (writes only):** double-submit. Send the `csrfToken` cookie **and** an
  `x-csrf` header with the same value.
- Most responses wrap the payload in `{"result": …}`. Unauthenticated requests
  return `{"result": null}` with HTTP 200.
- Common query params: `viewAs=student`, `scrub=true`.

### Login

`POST /api/sessions?scrub=true`

```json
// request (Google OAuth login)
{"googleToken": "<google-id-jwt>"}
// response
{"result": {"sessionId": "…", "userId": "…", "token": "<sessionToken>",
            "method": "google", "expires": {"$date": 1783088564839}}}
```

The web app then stores the signed `session` cookie. (The CLI's `login --browser`
captures that cookie directly instead of replaying this call.)

## Read endpoints

| Purpose | Request |
| --- | --- |
| Current user | `GET /api/users/current?scrub=true` |
| Balance | `GET /api/student_balances?studentId=<uid>&viewAs=student` |
| Favorite tutors | `GET /api/favorite_tutors?userId=<uid>&scrub=true` → `[{tutorId, active}]` |
| Tutor details | `GET /api/tutors?ids[]=<id>&ids[]=…&viewAs=student` → `{<id>: {...}}` |
| Tutor live status | `GET /api/tutor_statuses?viewAs=student` → `[{tutorId, isOnline, onshift, availableForMinutes}]` |
| Tutor schedule | `GET /getTutorSchedule?tutor=<id>&userId=<uid>&language=en&interfaceLanguage=en` |
| Tutor search | `POST /api/v2/algolia/search` (+ `GET /api/v2/algolia/index_config?queryType=tutor` for the index) |
| Upcoming lessons | `GET /api/lessons_v2?studentId=<uid>&minScheduledStartAt=<ms>&maxScheduledStartAt=<ms>&includeCancelled=false&viewAs=student` |
| Lesson detail | `GET /api/lessons_v2/<lessonId>?viewAs=student` |
| Lesson participants | `GET /api/lesson_participants?lessonId[]=<id>&includeCancelled=false&viewAs=student` |
| Lesson recording session | `GET /api/lessons_v2/<lessonId>/video_session_id` → `<videoSessionId>` |
| Recording metadata | `GET /api/video_sessions/<videoSessionId>?viewAs=student` → `{hasVideoUrl, provider, lessonId, …}` |
| Recording video | `GET /api/video_sessions/<videoSessionId>/video` → redirects/streams `video/mp4` |
| Lesson transcript | `GET /model/lesson_transcript/<lessonId>?language=en&interfaceLanguage=en` |
| Booking history (legacy) | `GET /api/reservations?studentId=<uid>&cancelled=false&end=<ms>&sort=-1&limit=50&viewAs=student` |
| Class recordings (legacy) | `GET /api/chats?language=en&userId=<uid>&role=student&viewAs=student` then `GET /api/chats/<chatId>/video` |

`/getTutorSchedule` returns `{"hasLibrary": bool, "schedule": [ {startTime:{$date}, endTime:{$date}, reservable: bool, tutorId} ]}`.

## Write endpoints

### Book — `POST /api/lessons_v2?viewAs=student`

```json
{"schedulingType": "reserved", "classSize": 1,
 "tutorId": "<id>", "studentId": "<uid>",
 "scheduledStartAt": 1780750800000, "scheduledEndAt": 1780752600000,
 "topic": ""}
```

Returns the created lesson (`{result: {id, state: "confirmed", scheduledStartAt, …}}`).
Fails `409 InsufficientMinutes` (`{"data":{"reason":"weeklyMinutes"}}`) when the
account has no available minutes — no booking is created.

### Cancel — two-step

1. Resolve the **student participant id** for the lesson via
   `GET /api/lesson_participants?lessonId[]=<lessonId>` (pick `role == "student"`,
   `userId == <uid>`). The participant id differs from the lesson id.
2. (optional) Check refund:
   `GET /api/lesson_participants/<participantId>/get_cancellation_refund_eligibility`
   → `{isFullRefund, minutesToRefund, lessonHasSubstituteTutor}`.
3. Cancel:
   `POST /api/lesson_participants/<participantId>/transition_to_cancelled?viewAs=student`
   ```json
   {"outcome": "user_cancelled"}
   ```
   Returns the participant with `state: "cancelled"`, `chargeState: "refunded"`.

## Reproducing the capture

The `recon/` directory contains the harness used to derive all of the above:

```sh
# 1. launch Chrome with the DevTools port + a throwaway profile
bash recon/launch-chrome.sh 9222
# 2. capture all Cambly XHR/Fetch traffic to recon/capture.jsonl
node recon/capture.mjs --port 9222
# 3. log in / click around in that Chrome window; inspect capture.jsonl with jq
```

`recon/capture.jsonl` contains request/response bodies **including auth tokens**,
so it is gitignored. Treat it as a secret.
