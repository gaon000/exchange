# 동시 요청·장애 조합 실제 검증

2026-11-17, master5558893 이후 기능 브랜치의 `--concurrent --seed 17`과 저장된 실제 순서 replay를 별도 새 폴더에서 실행했다. **실제 WAL 순서·스냅샷 경계, 재시작 전후 잔고·주문·호가·체결·최초 응답과 중복 방지 검증이 통과했다.** 정상 재시작이며 실제 OS·전원 장애 검증은 미완료·보류다. 처리량/지연 성능 측정이 아니다.

[검증 JSON](20261117-concurrent-recovery.json)과 [실행·명세·한계](../concurrent-recovery.md)를 함께 읽는다. source revision은5558893, 실행 binary는 작업 중 modified=true였고 각 build.json에 Go/플랫폼·실행 binary hash가 있다. 서로 다른 runId를 제외한 논리 상태를 비교하며 raw 파일 전체가 실행 간 동일하다고 주장하지 않는다.

## 실제 실행과 재현

```sh
sh scripts/random-recovery.sh --concurrent --seed 17 --output var/concurrent-fault-final-20261117
sh scripts/random-recovery.sh --replay var/concurrent-fault-final-20261117/replay-005.json --output var/concurrent-fault-final-replay-20261117
```

위 경로는 이미 보관됐으므로 다시 실행할 때는 새 output 이름을 사용한다. 각54개 파일의 manifest/completed hash와 실제 bytes를 확인했다. 보관 자료는 Git 제외 var에 있고 자동 JSON에는 각 manifest hash를 기록했다. 이전 초기 실행 `var/concurrent-fault-20261117`, `var/concurrent-fault-replay-20261117`도 보존한다.

| 검사 | 원래 실행 | 기록 순서 replay |
| --- | ---: | ---: |
| 클라이언트/동시 wave | 4/2 | 4/2 |
| 고유 WAL 명령/최초 결과 | 12/12 | 12/12 |
| 현재 주문/관측 체결 | 10/3 | 10/3 |
| phase 시도/동일 ID 중복 시도 | 16/8 | 16/8 |
| client 응답 유실/UNKNOWN | 6 | 6 |
| 보류 요청의404 UNKNOWN | 2 | 2 |
| 정상 재시작/WAL-only 전체 복구 | 3/3 | 3/3 |
| 첫 선택 snapshot+tail | 3+3 | 3+3 |
| 두 번째 선택 snapshot+tail | 9+3 | 9+3 |
| 마지막 선택 snapshot+tail | 12+0 | 12+0 |

초기 단계의 같은 주문 ID 두 복사본은 headers/eof와 read/unexpected_eof로 끊었고, 보류 요청은 headers/eof를 관측했다. 원래 client Outcome은 UNKNOWN으로 남는다. 중계의 upstream200이나 이후 조회/같은 ID 재시도가 이를 원래 시도의 성공으로 바꾸지 않는다. 404 UNKNOWN 역시 미접수 증거가 아니다.

선택한 응답이 아직 반환하지 않은 상태에서 snapshot을 만들고, 이후 retry와 늦은 원래 요청까지 합류했다. 재시작 전 HTTP 검사에서 사용한 임시 순서가 실제 WAL의 모든 입력/순번과 동일했다. 실제 WAL의 키가 계획과 일대일이며 과거 prefix/최초 결과도 유지됐다. snapshot 전체 상태가 해당 저장 prefix와 일치하고, 정상 복구/WAL-only의 전체 checkpoint·FIFO·자산 공급량·최초 결과 GET/POST를 확인했다. 복구와 검증 재시도가 WAL을 늘리지 않았다.

기록 replay에서는 runId와 tradeId의 run prefix를 정규화한 전체 최종 checkpoint 및 WAL 순서가 같았고 snapshot 경계도 같았다. 최종 replay-005.json은 bytes도 동일했다. 시간·포트·goroutine 실행 순서의 동일성을 주장하지 않는다. 자유 동시 실행은 유효한 WAL 순서에 따라 체결 결과가 달라질 수 있으며 위3건 체결은 이 실행의 관측이다.

## 회귀와 CI

native 회귀는 seed17/42/701 각 실제 실행과 recorded replay, 출력 실패 뒤 재현 명세 보존과 한 명령 재실행, 첫 신규 명령 뒤 snapshot을 강제해 나머지 초기 명령까지 tail에 남기는 경계도 검사했다. portable은 입력/예산·기록 순서 gate·순서 oracle·실제 중계 취소/합류·부분 replay 무시와 저장 미지원 시 무변경 실패를 포함한다.

최종 로컬 portable252개, native race295개(명시적 자식 helper skip1), 각 Python111개와 format/vet/build·진단 smoke가 통과했다. 원본은 `var/ci/portable.k1ss8Q`, `var/ci/native.xoBAal`에 보존한다. 새 성능 부하나 전원 장애를 실행하지 않았으며 커밋·푸시/GitHub CI는 이번 요청에 포함하지 않았다.
