# 스냅샷 정규형 비교용 버퍼 재사용

- 상태: Completed
- 종료: 2026-11-13
- 시작: 2026-11-13
- 기준: master 884c6cc83a6239db88a39d96a95c851090bf3209

## 목적·완료 조건

1. 배열 최적화 후 최신 생산 코드에서 같은 두 1만 명령 상태의 allocation profile을 각 5회 수집해 읽기/정규형 비교의 현재 할당 비중을 확인한다. profile 시간과 전후 성능 측정은 분리한다.
2. 표준 JSON 인코딩/정규형 비교를 유지하며 반환 bytes 사본을 줄인다. 기존 UTF-8·정수/null·필드·오류 우선순위·16KiB leaf/깊이64·32KiB 읽기·배열·payload 상한과 저장/복구 계약을 유지한다.
3. 같은 active/history 및 tail 0/1000/5000에서 두 버전 각 5회, 60개 새 worker로 Open 시간·TotalAlloc·GC 뒤 live heap/현재 RSS·peak RSS를 확인한다. 정확한 snapshot 선택/재생 수·최초 응답·재시도·후속 체결/취소와 전체 canonical 상태가 일치해야 한다.
4. 동결 source/archive·입력 hash·profile·원 결과/실패와 JSON/Markdown을 보존한다. 원본을 삭제하지 않고 성공 소유 scratch만 receipt 후 정리한다. 초기 128MiB 준비 여유·실행 중 8GiB 여유/4GiB 묶음·2GiB 측정 메모리 기준을 유지한다.
5. 차등 회귀·fuzz·로컬 portable/native CI와 새 폴더 재집계 bytes 일치를 확인하고 효과/한계를 기록한다. 커밋/발행은 이번 요청 범위가 아니다.

## 진행·판단

- 기존 array 비교 묶음의 봉인과 후보 생산 source가 현재 기준과 같은지 확인한 후 입력을 재사용한다. 프로파일링과 전후 비교는 새 작업 묶음 안의 공통 fixture를 공유한다.
- 실제 전원 장애 검증은 미완료·보류, v1 계획은 Blocked를 유지한다.

## 결과와 결정

- [x] 기준 884c6cc의 최신 allocation profile을 두 상태 각 5회·10개 새 worker에서 수집했다. 읽기 중 Marshal 누적 43.68/44.30%, 그 안의 bytes.Clone flat 29.69/30.49%를 확인했다. profile 시간/추정 할당과 전후 비계측 결과는 분리했다.
- [x] decoder별 표준 Encoder·bounded 비교 writer 재사용. 원문/전달 bytes 사본을 만들지 않고 framing 개행 하나·완전 일치/오류를 확인하며 입력 참조를 해제한다. JSON/저장/복구·16KiB 값/깊이64·읽기32KiB·배열·payload 상한 유지.
- [x] 두 상태·세 tail·두 버전 각 5회·60개 worker가 정확한 복구 경로·최초 응답 480개/재시도 120개·후속 거래/잔고/예약금·전체 canonical 상태/WAL 불변 검증 통과.
- [x] Open TotalAlloc 중앙값 모든 조건 6.07~9.37% 감소. GC 뒤 live heap 중앙값 차이 1.33KiB 이내, 현재 RSS는 5조건 감소/1조건 +1.266MiB. 후보 현재/peak RSS 모두 45.641MiB 이내로 기존 2GiB 측정 기준 유지. 시간 변화는 -3.48~+1.21%로 혼합이며 할당 감소 목적으로 채택.
- [x] source archive 기준/후보 각 261/262개·동일 Go probe/생산 변경 decode.go 하나 확인. 주 묶음 435파일/58.60MiB·입력 327파일 검증, 최신 profile 재생성 및 JSON/Markdown bytes 일치.
- [x] native race 277/portable 238 tests·각 Python 94·진단 smoke 통과. buffer fuzz 10,647/기존 leaf 차등 fuzz 365,801/reader fuzz 12,293회 통과.

profile과 비교가 공통 fixture를 공유하고 성공 소유 scratch만 receipt 후 정리했다. profile marker 소유권 검사를 최종 동결 전에 보강했으며 초기 smoke 후보도 별도로 보존했다. 전후 성능 비교가 끝난 뒤 재생성 가능한 프로젝트 Go 컴파일 캐시만 정리해 전체 CI 준비 공간을 확보했다. 실험 원본/고정 빌드/profile을 지우거나 공간·메모리 기준을 낮추지 않았다.

[결과](../../benchmarks/snapshot-canonical-buffer.md)·[재현/제한](../../snapshot-canonical-buffer.md)을 따른다. 이 계획의 완료 근거는 로컬 구현·프로파일링·비교·검증이며, 구현 완료 당시 커밋/병합/푸시/원격 CI는 수행하지 않았다. 후속 발행 상태는 해당 커밋과 CI 실행으로 별도 확인한다. 실제 전원 장애 미완료·보류 및 v1 Blocked 유지.

병합 전 검토: 생산 코드 추가 수정 없이 snapshot/statescale race·Python 94개와 원본/동결 source·새 profile/JSON/Markdown 재집계 일치를 확인했다.
