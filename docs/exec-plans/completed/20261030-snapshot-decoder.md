# 복구 디코더 최적화와 동일 상태 비교

상태: Completed
종료일: 2026-10-30

## Purpose / Big Picture

[단계 계측](../../benchmarks/snapshot-stage-breakdown.md)에서 가장 큰 비용이었던 snapshot decode의 반복 파싱·할당을 줄인다. 기존 입력 검증·저장 형식·복구/fallback/strict sync 계약을 유지하고, 동일 최종 상태에서 개선 전후 각 5회 비교한다. 생성 최적화·WAL 검사 생략·실제 전원 장애 시험은 포함하지 않는다.

## Progress

- [x] 2026-10-30: 이전 미커밋 변경 보존, feature branch 분리, 기준 소스 동결.
- [x] 2026-10-30: 닫힌 generic 타입 집합, canonical 검증과 16KiB 버퍼 재사용 구현. 6,325개 차등 입력·스키마 전제·크기/소유권·golden 검사 통과.
- [x] 2026-10-30: portable 190개·native race 222개와 두 fuzz 통과. 동일 상태 5쌍 모두 decode·전체 복구 개선, 전체 상태 bytes 일치.
- [x] 2026-10-30: 기준/후보 소스·바이너리·원본/집계 보존, 이전 363개 원본 파일 검증, 계약·보고서 기록.

## Surprises & Discoveries

현재 값 하나를 strictjson walk, shape, Unmarshal, canonical Marshal로 반복 처리한다. 스냅샷 leaf DTO에는 map/interface/slice가 없고 null 허용은 pointer와 Reason에 한정돼 있다. 이 제한을 명시하고 회귀 테스트로 고정했다. 기존 strictjson 기반 결과와 6,325개 deterministic 입력 및 차등 fuzz가 일치했다. 개선 후에도 WAL open/전체 대조와 snapshot 재확인 비용이 남아 있어 이를 생략한 성능으로 보고하지 않는다.

## Decision Log

- 2026-10-30: snapshot 내부의 닫힌 scalar/leaf 타입 집합에만 typed decode + 기존 canonical bytes 대조를 적용한다. 일반 HTTP/WAL strictjson은 유지한다. 각 값의 크기·깊이·UTF-8·정수·null 규칙과 기존 오류 분류를 유지한다.
- 2026-10-30: 값 버퍼는 decoder별로 최대 16KiB를 재사용한다. 문서 전체 buffering이나 전역 pool은 도입하지 않는다.
- 2026-10-30: 기준은 이번 변경 직전의 계측 구현으로 새로 빌드한다. 이전 실험 숫자를 개선 전 측정으로 재사용하지 않고 두 바이너리를 교차 실행한다.

- 2026-10-30: 정확성 검사와 5쌍의 decode/전체 복구 감소, 반복 범위 비중첩을 확인해 후보를 채택했다.

## Outcomes & Retrospective

후보 채택. 전체 복구 중앙값 3,767.219→2,543.226ms(32.5% 감소), decode 2,142.072→932.882ms(56.4% 감소), Open 직후 누적 할당량 45.5% 감소. 5쌍 모두 개선됐고 두 구간의 반복 범위가 겹치지 않았다. 10회 모두 전체 snapshot bytes·WAL/Genesis/lock 일치. 단일 frozen 상태·캐시를 비우지 않은 runtime 측정이며 동시 거래 p99·실제 전원 장애·다른 장비 결과로 확대하지 않는다. commit/merge/push는 하지 않았다.

## Context and Orientation

internal/snapshot/decode.go의 항목 파서가 대상이다. 배열 streaming, envelope/checksum, engine.Restore의 의미 검사, 전체 WAL 대조와 sync는 유지한다. 기존 strictjson 기반 검사를 테스트 oracle로 삼아 같은 입력의 허용/거절과 성공 값을 비교한다.

## Plan of Work

기준 바이너리/소스를 보존하고 닫힌 generic 타입 집합·canonical 비교의 전제·버퍼 재사용을 구현한다. 모든 leaf DTO 필드의 누락/중복/이름 변형/null, 숫자·UTF-8·길이·깊이·truncation을 검사한다. 기존 저장/복구 테스트, fuzz와 portable/native CI를 수행한 뒤 교차 반복한다.

## Validation and Acceptance

동일 51,385 순번 상태의 .lock/Genesis/WAL/최종 snapshot을 매번 새 디렉터리에 복사해 5쌍의 새 프로세스를 실행한다. 복사·해시는 측정 구간 밖이며 OS 캐시는 비우지 않는다. 단계별 시간·누적 할당·GC를 보고하고 전체 snapshot bytes/WAL 불변을 확인한다. decode 및 전체 복구 중앙값·반복 범위가 개선되고 검증 동등성이 유지되어야 채택한다. portable 190개/18패키지, native race 222개/18패키지(helper skip 1개)가 통과했다. 차등 fuzz 30초 1,582,336회와 snapshot reader fuzz 31초 21,455회에서 실패를 발견하지 못했다. 전체 복구와 decode의 중앙값·반복 범위가 모두 개선됐고 5쌍 각각도 개선됐다. 모든 run의 상태 SHA 및 기존 실험 234+129개 파일 SHA를 다시 확인했다. 후보의 source hash가 테스트/측정 당시와 일치함을 검증했다.

## Idempotence and Recovery

기존 원본을 수정하지 않는다. 새 고유 실험 폴더만 생성하고 기존 results 재사용은 거절한다. 실패도 보존한다. 성능/정확성 기준 미달이면 이 작업의 decoder 후보만 되돌리며 이전 사용자 변경은 보존한다.

## Artifacts and Notes

[비교 보고서](../../benchmarks/snapshot-decoder-optimization.md), [재집계](../../benchmarks/20261030-snapshot-decoder-comparison.json), [manifest 요약](../../benchmarks/20261030-snapshot-decoder-artifacts.json)에 조건·원본·판정을 연결한다. commit/merge/push는 요청되지 않았다.

## Interfaces and Dependencies

공개 API와 snapshot/WAL 포맷·계측 필드는 유지한다. 새 production dependency는 없다. 추가 전제는 snapshot의 제한된 leaf 타입과 canonical encoding이며 일반 JSON 수용 규칙으로 확대하지 않는다.
