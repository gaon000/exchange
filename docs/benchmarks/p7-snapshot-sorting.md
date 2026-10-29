# P7 스냅샷 정렬 이동과 비교

상태: In Progress — 정렬 이동 구현·회귀 검증 완료, 동일 조건의 성능 비교 미완료.
작성일: 2026-10-29 (Asia/Seoul).

## 선택 근거

[P6 기준 측정](p6-baseline.md)에서 정상 29,000 requests/s는 세 번 통과했지만 snapshot을 켠 같은 부하는 세 번 모두 지연·생성기 누락으로 탈락했다. owner의 상태 복사·정렬 최대는 656.942~706.851ms였고 진단 profile의 Engine.Capture 누적 CPU 표본 1.00초 중 sort.Slice가 0.69초였다. 명령을 처리하지 못하는 구간에서 정렬을 분리하는 것을 첫 개선으로 선택했다.

## 한 가지 변경

[CopyCheckpoint](../../internal/engine/checkpoint.go)는 명령 경계에서 가변 상태를 분리 복사한다. runtime은 복사본의 소유권을 기존 snapshot worker에 넘기고 다음 주문·조회를 처리한다. worker가 계정·주문·최초 결과 배열을 기존 기준으로 정렬한 뒤 파일을 게시한다. FIFO·체결 순서는 그대로 복사한다. 동기 Capture의 반환 순서, 저장 bytes, 복구 검증, WAL sync·성공 응답 순서와 동시 사본 한 개 제한은 유지한다.

상세 계약은 [저장 설계](../design-docs/storage-v1.md#5-snapshot-dto와-게시)가 소유한다. 외부 의존성·새 worker·새 실행 옵션은 추가하지 않았다. 아직 복사 자체의 할당·GC나 snapshot 직렬화를 최적화하지 않았다.

snapshotCapture 계측은 owner의 복사만 포함하고, 정렬 비용은 snapshotPublish의 worker 구간으로 이동한다. capture 시간 감소만으로 전체 CPU/게시 비용이나 p99가 줄었다고 판단하지 않는다. 두 구간·전체 요청 지연·누락·메모리를 함께 비교한다. P6 원본의 capture는 복사와 정렬을 모두 포함한다.

## 회귀 검증

- 독립 golden checkpoint JSON과 snapshot envelope, 손상된 순서 거절·저장/복구 테스트를 유지했다. CopyCheckpoint를 나중에 정렬한 결과도 golden bytes와 일치했다.
- 복사 이후 실시간 체결·취소가 진행돼도 사본의 상태/최초 결과/FIFO가 원래 경계로 복구됨을 확인했다. Restore는 저장 파일의 비정규 순서를 정렬해 수리하지 않는다.
- 테스트 hook으로 worker를 정렬 직전에 멈춘 상태에서 주문·조회 완료, 두 번째 snapshot 거절, Shutdown 대기와 저장소 잠금 유지, 정렬 재개 후 과거 경계 게시를 확인했다.
- worker 정렬 경계의 panic을 주입했을 때 게시·순번 전진 없이 INTERNAL_FAILURE로 중단하고 저장소를 닫았다. 파일 게시·복구·HTTP 장애/프로세스 종료 검증도 전체 race 실행에 포함했다.
- `scripts/go test ./... -timeout=180s`, `scripts/go test -race ./... -timeout=180s`, `CGO_ENABLED=0 scripts/go test ./internal/engine ./internal/runtime ./internal/snapshot -timeout=120s`, 전체 vet/build가 통과했다. cgo 비활성 검사는 실제 APFS 검증을 대신하지 않는다.

## 비교 조건과 남은 결과

[고정 조건 v1](p6-baseline.md#고정-비교-조건-v1)을 유지한다. clean commit의 서버를 사용하고 P6 기준 생성기·실행기·판정 도구를 고정해 서버의 정렬 위치만 비교한다. 우선 29,000 requests/s에서 정상·snapshot을 각각 세 번 새 run으로 측정하고 snapshot-profile 진단을 따로 수행한다. 실패 실행을 보존하며 기준을 완화하지 않는다. 정상 조건의 회귀와 snapshot 중 지연·누락을 별도 판정한다.

아직 변경 후 성능 수치나 개선률은 없다. 측정 자료를 수집한 뒤 소스 revision·바이너리/입력 해시·원본 위치와 최종 판정을 기록한다. 실제 전원 장애는 별도 장비가 없어 계속 미완료이며 전체 계획의 완료 조건은 유지한다.
