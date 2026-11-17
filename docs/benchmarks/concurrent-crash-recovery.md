# 동시 요청 SIGKILL·복구 실제 검증

2026-11-17, master1c204d2 이후 작업 브랜치에서 세 경계의 실제 자식 SIGKILL·복구와 기록 replay가 모두 통과했다. 각 경계의 네 미응답 호출은 UNKNOWN으로 남기고, 실제 WAL 순서와 복구 전후 전체 checkpoint·잔고·호가/FIFO·체결·최초 응답 및 같은 ID 재시도의 중복 방지를 확인했다. 실제 OS·전원 장애 검증은 계속 미완료·보류다. 성능 측정은 하지 않았다.

[검증 JSON](20261117-concurrent-crash-recovery.json), [명령·계약과 한계](../concurrent-crash-recovery.md)를 함께 읽는다. 소스는 기준1c204d2에서 수정 중인 상태다. test binary는 Go1.27.3 darwin/arm64/cgo이며 기존 test-only hook을 사용한다. 일반 배포 서버 바이너리에 장애 옵션을 추가하지 않았다.

## 한 명령 실행과 기록 replay

```sh
sh scripts/concurrent-crash.sh --seed 17 --output var/concurrent-crash-final-20261117
sh scripts/concurrent-crash.sh --replay var/concurrent-crash-final-20261117/replay-005.json --output var/concurrent-crash-final-replay-20261117
```

두 출력은 이미 존재하므로 재실행할 때 새 이름을 사용한다. 원본/재현은 각각84개 봉인 파일의 실제 bytes/크기/hash와 manifest/completed를 검증했다. 두 실행의 test binary hash는 `c7135b2fd3053d26ab9605c83332b64c8f5a87c0a1757b14e2b8b55e925fc630`으로 같다. 초기 실행/재현·개발 중 회귀 실패 로그도 별도로 보존했다. 원본과 거래 저장소는 Git 제외 var, 요약과 manifest hash는 위 JSON에 있다.

| 경계 | 종료 직후 WAL 명령 | 선택 snapshot + tail | 같은 ID 재시도 후 고유 명령 | 원래 UNKNOWN | 후속404 UNKNOWN | 관측 체결 |
| --- | ---: | --- | ---: | ---: | ---: | ---: |
| WAL 실제 동기화 뒤 적용/응답 전 | 3 | 2 + 1 | 5 | 4 | 2 | 2 |
| snapshot 실제 Publish 호출 전 | 6 | 2 + 4 | 6 | 4 | 0 | 3 |
| snapshot 실제 Publish 성공 뒤 완료 통보 전 | 6 | 5 + 1 | 6 | 4 | 0 | 3 |

원본과 기록 replay에서 위 수치가 같았다. 체결 수는 이 실행에서 관측한 저장 순서의 결과이며 모든 자유 동시 실행의 고정 기대값이 아니다. 각 case는 독립 run/초기 잔고를 사용한다. 초기 매도/부분 매수의 성공 응답6개와 snapshot 대기 중 후속 매도 성공 응답2개가 실제 복구된 WAL/최초 응답과 일치했다.

네 HTTP 요청이 자식의 공통 gate에 모두 도착한 뒤 열렸다. 죽이기 직전 네 호출이 계속 진행 중임을 확인했고, 실제 소유 자식 PID의 WaitStatus가 SIGKILL이었다. WAL 경계는 정상 Store.Append가 성공한 직후에 멈춰 bytes 주입이나 약한 동기화로 대신하지 않았다. snapshot 전/후 경계의 worker가 멈춘 동안 후속 명령을 성공 처리하여, 이전 snapshot 또는 새 snapshot 모두 이후 WAL을 포함해 복구하는 것을 확인했다.

복구 후 첫 GET은 재시도보다 먼저 수행했다. WAL 경계의 두 키는404 UNKNOWN, 나머지는 저장된 최초 결과를 반환했다. 이 후속 관측은 원래12건 UNKNOWN을 성공/미접수로 덮어쓰지 않는다. 404 자체로 미접수를 단정하지 않고 같은 ID/내용을 재시도했다. snapshot/조회는 WAL을 바꾸지 않았고, 기존 prefix·최초 응답을 유지한 채 없던 두 고유 키만 한 번씩 추가됐다.

이후 각 case를 다시 정상 재기동하고 최초 결과를 GET으로 확인한 뒤 고유 ID 총17개를 다시 POST했다. 전체 WAL bytes·체결/잔고/예약금과 최초 결과가 유지됐다. 세 경계의 실제 crash/final WAL 순서 및 최종 전체 checkpoint는 runId·tradeId의 run prefix를 정규화했을 때 원본/재현에서 동일했다. 최종 replay-005.json은 bytes도 동일했다. PID·포트·시간·OS 실행 순서가 동일하다는 뜻은 아니다.

## 회귀와 전체 로컬 CI

- seed17/42 각 세 경계의 실제 SIGKILL + 기록 replay: 저장 순서/전체 상태/최초 응답 일치.
- seed701 실행의 마지막 출력 실패: failed.json·기록 replay 보존, completed 미게시, 새 폴더 재현 성공.
- 네 요청이 진행 중이고 실제 Append 경계에서 멈춘 자식의 취소: 자식 SIGKILL/reap·클라이언트 합류·WAL 잠금 재획득.
- 잘못된 WAL 키/내용·중복/순번과 이전 prefix 변조 거절. seed/명세·재현 순서·기존/중첩 출력 보호·부분 replay 무시. 저장 미지원에서는 출력 생성 전 실패.
- `bash scripts/ci.sh portable`: `var/ci/portable.PpotZe`, Go255개·Python111개, format/vet/build·진단 smoke exit0.
- `bash scripts/ci.sh native`: `var/ci/native.bQ2WAu`, Go race301개(명시적 자식 helper skip1)·Python111개, 저장 probe·전체 기존 프로세스/HTTP·전송 보고서·진단 smoke exit0.

최초 컴파일의 Outcome 필드 오류와 macOS 임시 디렉터리의 /var symlink 거절은 수정하고 재검증했다. 첫 두 회귀 실패 로그와 이후 통과 로그는 var/concurrent-crash-*-tests.log에 보존한다. 소스 수정 후 전체 두 CI와 사용자용 최종 원본/replay를 확인했다. 커밋·푸시·새 GitHub CI는 이번 구현 요청에 포함하지 않았다.

기존 runtime test bridge와 동일한 매칭 엔진을 비교 모델로 사용한다. 독립 매칭 oracle·프로세스 외 복제·무제한 장기 운전·전원 장애 내구성을 새로 입증하지 않는다. OS/장치 캐시는 SIGKILL 뒤에도 살아 있으므로 실제 OS·전원 장애는 v1 계획의 미완료·보류 항목으로 유지한다.
