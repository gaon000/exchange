# 전송 오류·UNKNOWN 분석

측정 당시 UNKNOWN과 후속 조회는 별개다. 404 UNKNOWN·202 PENDING은 미확정이며 미접수 증거가 아니다.
후속 200은 같은 키의 결과 관측이며 원래 전송 시도의 성공/접수 시점이나 성능 판정을 바꾸지 않는다.
누락된 단계/유형·지연은 미계측이다. 장애 재현과 부하 측정의 수치를 합쳐 성능으로 해석하지 않는다.

## 2026-11-16 10분 snapshot 끔

종류: loadgen, run: 2d720a2e-6317-4bfb-aa1c-f9fa170bb6e7

원래 measurementValid: False; 본 측정 UNKNOWN 논리 명령: 0

| phase | 응답 분류 | 실패 단계 | 오류 유형 | 시도 | 논리 키 | 지연 관측/누락 | p50 ms | p99 ms | 최대 ms |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| — | 전송 실패 없음 | — | — | 0 | 0 | — | — | — | — |

| phase/index | 계정/종류/요청 ID | 측정 당시 | 분류·단계·유형 | 요청 지연 ms |
| --- | --- | --- | --- | ---: |

| 계정/종류/요청 ID | 실험 내 완전 응답 관측 | 후속 조회 결과 관측 |
| --- | --- | --- |

| 요청 ID | 조회 HTTP/상태 | 해당 조회 해석 | step/line | UTC |
| --- | --- | --- | --- | --- |

## 2026-11-16 10분 snapshot 켬

종류: loadgen, run: 2ea8a818-ee3b-4c07-aa82-32d528b2f9a3

원래 measurementValid: False; 본 측정 UNKNOWN 논리 명령: 1

| phase | 응답 분류 | 실패 단계 | 오류 유형 | 시도 | 논리 키 | 지연 관측/누락 | p50 ms | p99 ms | 최대 ms |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| MEASURE | TRANSPORT_FAILURE | 미계측 | 미계측 | 1 | 1 | 1/0 | 4.662542 | 4.662542 | 4.662542 |

| phase/index | 계정/종류/요청 ID | 측정 당시 | 분류·단계·유형 | 요청 지연 ms |
| --- | --- | --- | --- | ---: |
| MEASURE/38457 | bob/place/soak-v1-o-38457 | UNKNOWN | TRANSPORT_FAILURE/미계측/미계측 | 4.662542 |

| 계정/종류/요청 ID | 실험 내 완전 응답 관측 | 후속 조회 결과 관측 |
| --- | --- | --- |
| bob/place/soak-v1-o-38457 | UNRESOLVED | UNRESOLVED |

| 요청 ID | 조회 HTTP/상태 | 해당 조회 해석 | step/line | UTC |
| --- | --- | --- | --- | --- |
| soak-v1-o-38457 | 404/UNKNOWN | UNRESOLVED | —/— | 미계측 |

## 기존 장애 재현 (지연 미계측)

종류: recoverycheck, run: b5368915-7f2e-4545-b8e8-b4afeadb4431

원래 measurementValid: 미계측; 본 측정 UNKNOWN 논리 명령: 미계측

| phase | 응답 분류 | 실패 단계 | 오류 유형 | 시도 | 논리 키 | 지연 관측/누락 | p50 ms | p99 ms | 최대 ms |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| FAULT_SCENARIO | TRANSPORT_FAILURE | response_headers | eof | 2 | 2 | 0/2 | 미계측 | 미계측 | 미계측 |
| FAULT_SCENARIO | TRANSPORT_FAILURE | response_read | unexpected_eof | 1 | 1 | 0/1 | 미계측 | 미계측 | 미계측 |

| phase/index | 계정/종류/요청 ID | 측정 당시 | 분류·단계·유형 | 요청 지연 ms |
| --- | --- | --- | --- | ---: |
| FAULT_SCENARIO/1 | bob/place/loss-taker | UNKNOWN | TRANSPORT_FAILURE/response_headers/eof | 미계측 |
| FAULT_SCENARIO/3 | alice/cancel/loss-cancel | UNKNOWN | TRANSPORT_FAILURE/response_headers/eof | 미계측 |
| FAULT_SCENARIO/5 | bob/place/loss-rest | UNKNOWN | TRANSPORT_FAILURE/response_read/unexpected_eof | 미계측 |

| 계정/종류/요청 ID | 실험 내 완전 응답 관측 | 후속 조회 결과 관측 |
| --- | --- | --- |
| alice/cancel/loss-cancel | UNRESOLVED | RESULT_OBSERVED |
| bob/place/loss-rest | UNRESOLVED | RESULT_OBSERVED |
| bob/place/loss-taker | UNRESOLVED | RESULT_OBSERVED |

| 요청 ID | 조회 HTTP/상태 | 해당 조회 해석 | step/line | UTC |
| --- | --- | --- | --- | --- |
| loss-cancel | 200/COMPLETED | RESULT_OBSERVED | 3/39 | 미계측 |
| loss-cancel | 200/COMPLETED | RESULT_OBSERVED | 3/52 | 미계측 |
| loss-cancel | 200/COMPLETED | RESULT_OBSERVED | 4/65 | 미계측 |
| loss-cancel | 200/COMPLETED | RESULT_OBSERVED | 4/78 | 미계측 |
| loss-cancel | 200/COMPLETED | RESULT_OBSERVED | 5/94 | 미계측 |
| loss-cancel | 200/COMPLETED | RESULT_OBSERVED | 7/112 | 미계측 |
| loss-cancel | 200/COMPLETED | RESULT_OBSERVED | 7/131 | 미계측 |
| loss-cancel | 200/COMPLETED | RESULT_OBSERVED | 7/150 | 미계측 |
| loss-rest | 200/COMPLETED | RESULT_OBSERVED | 5/80 | 미계측 |
| loss-rest | 200/COMPLETED | RESULT_OBSERVED | 5/96 | 미계측 |
| loss-rest | 200/COMPLETED | RESULT_OBSERVED | 7/114 | 미계측 |
| loss-rest | 200/COMPLETED | RESULT_OBSERVED | 7/133 | 미계측 |
| loss-rest | 200/COMPLETED | RESULT_OBSERVED | 7/152 | 미계측 |
| loss-taker | 404/UNKNOWN | UNRESOLVED | 1/2 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 1/4 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 1/15 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 2/26 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 2/37 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 3/50 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 4/63 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 4/76 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 5/92 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 7/110 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 7/129 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 7/148 | 미계측 |

## 2026-11-17 장애 재현 (지연 계측)

종류: recoverycheck, run: c163af03-fe8b-4ca9-ac92-fd942aa54682

원래 measurementValid: 미계측; 본 측정 UNKNOWN 논리 명령: 미계측

| phase | 응답 분류 | 실패 단계 | 오류 유형 | 시도 | 논리 키 | 지연 관측/누락 | p50 ms | p99 ms | 최대 ms |
| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| FAULT_SCENARIO | TRANSPORT_FAILURE | response_headers | eof | 2 | 2 | 2/0 | 0.498125 | 4.213875 | 4.213875 |
| FAULT_SCENARIO | TRANSPORT_FAILURE | response_read | unexpected_eof | 1 | 1 | 1/0 | 4.005042 | 4.005042 | 4.005042 |

| phase/index | 계정/종류/요청 ID | 측정 당시 | 분류·단계·유형 | 요청 지연 ms |
| --- | --- | --- | --- | ---: |
| FAULT_SCENARIO/1 | bob/place/loss-taker | UNKNOWN | TRANSPORT_FAILURE/response_headers/eof | 0.498125 |
| FAULT_SCENARIO/3 | alice/cancel/loss-cancel | UNKNOWN | TRANSPORT_FAILURE/response_headers/eof | 4.213875 |
| FAULT_SCENARIO/5 | bob/place/loss-rest | UNKNOWN | TRANSPORT_FAILURE/response_read/unexpected_eof | 4.005042 |

| 계정/종류/요청 ID | 실험 내 완전 응답 관측 | 후속 조회 결과 관측 |
| --- | --- | --- |
| alice/cancel/loss-cancel | UNRESOLVED | RESULT_OBSERVED |
| bob/place/loss-rest | UNRESOLVED | RESULT_OBSERVED |
| bob/place/loss-taker | UNRESOLVED | RESULT_OBSERVED |

| 요청 ID | 조회 HTTP/상태 | 해당 조회 해석 | step/line | UTC |
| --- | --- | --- | --- | --- |
| loss-cancel | 200/COMPLETED | RESULT_OBSERVED | 3/39 | 미계측 |
| loss-cancel | 200/COMPLETED | RESULT_OBSERVED | 3/52 | 미계측 |
| loss-cancel | 200/COMPLETED | RESULT_OBSERVED | 4/65 | 미계측 |
| loss-cancel | 200/COMPLETED | RESULT_OBSERVED | 4/78 | 미계측 |
| loss-cancel | 200/COMPLETED | RESULT_OBSERVED | 5/94 | 미계측 |
| loss-cancel | 200/COMPLETED | RESULT_OBSERVED | 7/112 | 미계측 |
| loss-cancel | 200/COMPLETED | RESULT_OBSERVED | 7/131 | 미계측 |
| loss-cancel | 200/COMPLETED | RESULT_OBSERVED | 7/150 | 미계측 |
| loss-rest | 200/COMPLETED | RESULT_OBSERVED | 5/80 | 미계측 |
| loss-rest | 200/COMPLETED | RESULT_OBSERVED | 5/96 | 미계측 |
| loss-rest | 200/COMPLETED | RESULT_OBSERVED | 7/114 | 미계측 |
| loss-rest | 200/COMPLETED | RESULT_OBSERVED | 7/133 | 미계측 |
| loss-rest | 200/COMPLETED | RESULT_OBSERVED | 7/152 | 미계측 |
| loss-taker | 404/UNKNOWN | UNRESOLVED | 1/2 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 1/4 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 1/15 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 2/26 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 2/37 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 3/50 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 4/63 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 4/76 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 5/92 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 7/110 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 7/129 | 미계측 |
| loss-taker | 200/COMPLETED | RESULT_OBSERVED | 7/148 | 미계측 |

실제 전원 장애 검증: 미완료·보류.원본·성공 건수·실패율·measurementValid를 수정하지 않는다.
