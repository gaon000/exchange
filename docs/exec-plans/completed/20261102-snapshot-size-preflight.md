# 스냅샷 생성의 중복 직렬화 제거

상태: Completed
종료일: 2026-11-02

[ExecPlan 운영 규칙](../index.md)을 따른다.

## Purpose / Big Picture

스냅샷 생성 전 크기 검사에서 같은 상태를 JSON으로 직렬화하는 비용을 줄인다. 저장·복구 계약은 유지하고 동일한 고정 상태에서 기존 구현과 후보를 각각 5회 측정한다. 원본 실험 자료와 기존 결과는 보존한다. 전원 장애 실험과 처리량 재측정은 범위 밖이다.

## Progress

- [x] 2026-11-02: master `df71229`와 원본 실험 상태, 저장 계약 확인. feature branch에서 시작.
- [x] 2026-11-02: 정확한 JSON 길이 계산과 차등·경계·실패 검증 구현.
- [x] 2026-11-02: portable 195개/native race 227개, 30초 fuzz와 동일 상태 5쌍·재시작·양방향 버전 복구 검증.
- [x] 2026-11-02: 비교 보고·source/binary/원본 hash·CI 근거 보존 및 현재 문서 갱신.

## Surprises & Discoveries

`measure`는 전체 payload를 버리지 않고 세지만 leaf마다 `json.Marshal`을 실행한다. 이어지는 실제 쓰기에서 같은 직렬화가 반복된다. 기존 크기 검사는 filesystem 접근보다 먼저 수행되므로 단순 제거나 파일 생성 후 검사는 계약에 맞지 않는다. 기존 decoder 실험의 Snapshot 호출은 이미 존재하는 파일의 재확인이었으므로 생성 시간 비교에 재사용하지 않는다.

초기 차등 검사에서 잘못된 UTF-8의 replacement bytes 길이 가정을 검출했다. 해당 입력은 Marshal 자체로 되돌려 기존 처리를 보존한 뒤 전체 검사를 통과했다. 측정 2회차 후보는 정렬·사전 검사·쓰기 등 여러 구간이 함께 느려졌으나 원인은 확정하지 못했고 표본을 그대로 유지했다.

## Decision Log

- 2026-11-02: 닫힌 scalar/leaf DTO의 JSON 길이만 계산하고 실제 writer는 유지한다. 지원하지 않는 타입·codec은 기존 Marshal로 돌아간다. 전체 JSON을 메모리에 보관하지 않는다.
- 2026-11-02: seq 51,385 원본의 Genesis/WAL/.lock만 복사한 새 디렉터리에서 생성한다. 전후 순서를 교대하며 각각 새 프로세스로 5회 측정한다. 생성 파일 SHA-256을 원본과 대조하고 별도 재시작에서도 snapshot 복구와 재확인을 검증한다.
- 2026-11-02: 5쌍 중 4쌍의 개선, 생성 중앙값 29.7%·할당량 39.6% 감소와 계약 검증을 근거로 후보를 채택한다. 2회차 지연 증가를 제외하지 않고 최악 시간·동시 부하 개선은 주장하지 않는다.

## Outcomes & Retrospective

생성 중앙값 675.941ms → 475.324ms, 사전 검사 248.935ms → 51.277ms, Snapshot 중 할당량 302.543MB → 182.755MB다. 전체 10개 파일이 원본과 일치했고 10회 재시작 및 양방향 버전 복구 2회가 성공했다. 실제 인코딩/쓰기 중앙값 365ms가 남은 주요 비용이며 owner 복사·정렬은 바꾸지 않았다. 단일 상태·OS 캐시 조건의 결과다. 동시 부하와 실제 전원 장애는 이번 범위 밖이며 기존 v1 계획의 보류 상태를 유지한다. 커밋·merge·push는 하지 않았다.

## Context and Orientation

[코덱](../../../internal/snapshot/codec.go)의 `measure`는 Write/Publish/Confirm이 공유한다. [게시 경로](../../../internal/snapshot/publish.go)는 사전 검사 뒤 temp 쓰기 → strict sync → close → exclusive rename → directory sync를 수행한다. [저장 계약](../../design-docs/storage-v1.md)의 envelope, SHA-256, 2 GiB/16 KiB 제한, 복구 의미 검사와 오류 분류를 유지한다.

## Plan of Work

기존 source와 측정 binary를 먼저 보존한다. leaf 길이 계산을 공통 payload 순회에 연결하고 표준 JSON을 기준으로 차등 검증한다. 전체 검사 후 후보를 고정하여 동일 상태의 새 파일 생성과 별도 재시작을 5쌍 비교한다. 단계 시간·메모리·원본 hash를 보고한다.

## Validation and Acceptance

저장소 루트에서 snapshot 테스트와 크기 계산 fuzz, `bash scripts/ci.sh portable`, `bash scripts/ci.sh native`를 실행한다. 기존 golden bytes, 파일 조작 전 오류 거절, 오류 우선순위, short write·sync·rename·복구 실패 경계를 검증한다. 비교는 새 파일 생성의 `publish.measure`·`publish.encodeWrite` 및 전체 Snapshot 시간을 구분하고 모든 결과의 상태·파일 hash를 대조한다. 문서 링크와 `git diff --check`를 확인한다.

실제 결과: `scripts/go test ./internal/snapshot` 통과. 8,000개 leaf/100개 payload × 6개 제한의 차등 검사, preflight fuzz 설정 30초·보고 9,893회 통과. portable 195개/18패키지, native race 227개/18패키지·의도한 helper skip 1개 통과. 측정 소스와 현재 Go/스크립트 hash 일치. 새 파일 생성 10회 모두 원본 hash 일치 및 저장·복구 검증 통과. 이번 GitHub CI는 실행하지 않았다.

## Idempotence and Recovery

실험은 원본을 변경하지 않고 새로운 사본에서만 실행한다. 기존 snapshot을 덮어쓰지 않으며 실패한 실험도 근거로 보존한다. 후보는 기존 writer와 같은 bytes를 내야 하며 이전 버전과의 복구 호환성을 유지한다.

## Artifacts and Notes

원본은 [단계별 계측 보고](../../benchmarks/snapshot-stage-breakdown.md)의 seq 51,385 상태다. 이전 [decoder 개선](../../benchmarks/snapshot-decoder-optimization.md)은 이번 baseline에 포함된다. 상세 원본 경로·hash·명령은 새 비교 보고에 남긴다.

[최종 비교](../../benchmarks/snapshot-preflight-optimization.md), [집계 JSON](../../benchmarks/20261102-snapshot-preflight-comparison.json), [124개 원본 manifest](../../benchmarks/20261102-snapshot-preflight-artifacts.json)에 실행 조건·모든 표본·검증과 한계를 기록했다. 원본은 Git 제외 경로 `var/snapshot-preflight-20261102.ztj_vssr`에 보존한다.

## Interfaces and Dependencies

공개 API·저장 형식·설정·동기화 순서는 바꾸지 않는다. 새 production dependency 없음.
