# 디코더 읽기 버퍼 32/64/128/256KiB 대조

상태: Completed
종료일: 2026-11-13

## Purpose / Big Picture

master 104896e의 생산 코드와 snapshot-tail의 같은 두 상태/1만 명령에서 decoder 읽기 버퍼만 32/64/128/256KiB로 바꿔 tail 0/1천/5천 각 5회·120번 공개 Open을 비교한다. 기본 생산 파일은 바꾸지 않고 Go build overlay로 decode.go의 NewReaderSize 인자 한 곳만 대체한 네 실행 파일을 동결한다. checksum/쓰기/confirm 버퍼·검증·복구·동기화 계약은 유지한다.

## Progress

- [x] 코드/계약/기존 자료/공간 확인, 브랜치 생성.
- [x] 시간 밖 ReaderAt·pread 진입 계측과 변형 빌드/집계·회귀 구현.
- [x] 네 크기의 reader 계약 회귀·최종 CI/smoke 확인.
- [x] 120회 측정·후속 거래/최초 응답/전체 상태·IO 대조/재집계 검증.
- [x] JSON/Markdown 결과·원본/빌드 근거·한계와 채택 판단 정리.

## Decision Log

- 2026-11-13: public Open 시간/메모리에는 관찰자를 넣지 않는다. 성공한 Open 검증 뒤 선택 prefix snapshot을 public Read로 다시 읽으며 ReaderAt 요청과 직접 syscall.Pread 호출을 별도로 센다. 이는 Open 자체의 커널 trace가 아니고 같은 파일/reader 알고리즘의 별도 진단 실행이다.
- 2026-11-13: 일반 File.ReadAt 경로와 직접 Pread 경로의 문서/요청·bytes를 대조한다. EINTR/short read는 별도로 기록하고 두 checksum·16KiB leaf/깊이/오류 계약을 생략하지 않는다.
- 2026-11-13: 8GiB 여유/4GiB 실험 예산을 유지한다. 새 worker 임시 사본은 성공 결과/전체 파일 hash+size receipt를 보존한 뒤 소유권·파일 집합·hash를 확인해 정리한다. 실패 사본/공통 fixture/기존 자료는 삭제하지 않는다. 사본은 공통 fixture에서 재구성 가능하다.

## Validation and Acceptance

네 overlay의 허용 차이가 decoder 버퍼 인자 한 곳뿐인지 확인한다. 정상/손상/golden·변경 reader·EOF/오류/크기·canonical 계약 회귀와 native 복구/전체 CI를 실행한다. 실제 파일에서 header/checksum/digest/decode 단계별 요청 수/크기/bytes와 Pread 재시도·완전한 상태/첫 응답/중복 재시도·tail 거래/잔고를 확인한다. 5회 표본과 느린 표본을 유지하고 새 폴더 재집계 bytes를 비교한다. 실제 전원 장애/HTTP p99는 검증하지 않는다.

## Outcomes & Retrospective

120회 모두 정확한 복구 경로·전체 상태 bytes·960개 최초 응답/240회 재시도·후속 체결/취소/예약 반환 검증을 통과했다. 네 크기의 ReaderAt/Pread 문서/요청·bytes가 일치했고 EINTR/short read는 0이었다. active tail 0의 32→256에서 decode 요청 296→37, 전체 진단 Pread 594→335로 줄었으나 Open 중앙값은 311.285→324.225ms로 높았다. 다른 조건도 시간 변화가 혼합돼 더 큰 버퍼는 미채택, 생산 기본값 32KiB를 유지한다.

생산 파일은 그대로이며 decoder 인자와 실험 테스트의 read 상한만 overlay로 치환했다. 최초 손계산 기대 횟수/고정 32KiB test source 실패를 보존·수정했다. 네 overlay의 snapshot/runtime race 회귀, 32/256 reader fuzz 120,582/473,957회와 최종 portable 234/native race 273·각 Python 83·진단 smoke가 통과했다.

본 묶음은 757개 파일/69.30MiB, source archive 255개·기존 입력 314개 파일/120개 결과/receipt를 검증했다. 성공 scratch 사본만 정리하고 기존 원본과 공통 fixture/고정 빌드를 보존했다. 새 JSON/Markdown 재집계 bytes가 같고 대표 8개 사본도 재구성·재실행에 통과했다. [결과와 한계](../../benchmarks/decoder-read-buffer.md), [명령/계수/보존 범위](../../decoder-read-buffer.md)를 따른다.

병합 전 검토에서도 코드 변경 없이 원본/동결 source·Python 83개·수집기/CI 검증기 race·새 재집계 JSON/Markdown bytes 일치를 확인했다. 위 기록은 구현/수집·검토 시점의 로컬 근거이며 후속 커밋·병합·푸시·해당 master의 원격 CI는 별도로 확인한다. 실제 전원 장애는 미완료·보류, v1 전체 Blocked 유지.
