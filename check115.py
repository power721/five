#!/usr/bin/env python3
import re, json, time, urllib.request, urllib.parse, urllib.error

UA = ("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 115Browser/35.17.0 Chromium/125.0.0.0")
API = "https://115cdn.com/webapi/share/snap"
INTERVAL = 5          # 每个请求间隔秒数
ABORT_ON_405 = 3      # 连续 N 个 405 视为风控，中止

def parse_line(line):
    m = re.match(r'^(s[a-z0-9]+)\?password=([^\s]+)', line)
    return (m.group(1), m.group(2)) if m else None

def check(code, pwd):
    q = urllib.parse.urlencode({
        "share_code": code, "offset": 0, "limit": 20, "asc": 0,
        "cid": 0, "receive_code": pwd, "format": "json",
    })
    url = f"{API}?{q}"
    req = urllib.request.Request(url, headers={
        "accept": "*/*", "accept-language": "en-US,en;q=0.9",
        "referer": f"https://115cdn.com/s/{code}?password={pwd}",
        "user-agent": UA,
    })
    try:
        raw = urllib.request.urlopen(req, timeout=20).read().decode("utf-8", "ignore")
        d = json.loads(raw)
    except urllib.error.HTTPError as e:
        if e.code == 405:
            return ("RATE", "HTTP 405 风控")   # 不重试
        return ("ERROR", f"HTTP {e.code}")
    except Exception as e:
        return ("ERROR", f"异常: {e}")
    state = d.get("state"); errno = d.get("errno"); err = d.get("error", "")
    data = d.get("data", {}) or {}
    si = data.get("shareinfo", {}) or {}
    share_state = si.get("share_state")
    forbid = si.get("forbid_reason", "")
    if state is True and errno == 0 and share_state == 1:
        return ("OK", f"title={si.get('share_title','')!r}")
    if "提取码" in err or "密码" in err or errno in (990002, 990003):
        return ("PWD?", f"提取码错误 errno={errno} {err!r}")
    return ("DEAD", f"errno={errno} ss={share_state} {err!r} {forbid!r}")

def main():
    with open("/home/user/workspace/five/3.txt", encoding="utf-8") as f:
        lines = [l.rstrip("\n") for l in f if l.strip()]
    results = []
    consecutive_405 = 0
    aborted = False
    for i, ln in enumerate(lines, 1):
        p = parse_line(ln)
        if not p:
            print(f"[{i:>3}] PARSE_FAIL  {ln}"); continue
        if i > 1:   # 每次请求前等待
            time.sleep(INTERVAL)
        code, pwd = p
        status, note = check(code, pwd)
        results.append((i, ln, code, status, note))
        print(f"[{i:>3}] {status:5} {code}  | {note}", flush=True)
        if status == "RATE":
            consecutive_405 += 1
            if consecutive_405 >= ABORT_ON_405:
                print(f"\n!! 连续 {ABORT_ON_405} 个 405，判定已触发风控，中止后续请求 !!")
                aborted = True
                break
        else:
            consecutive_405 = 0
    if aborted:
        print("(部分条目未检查，因风控中止)")
    counts = {}
    for r in results:
        counts[r[3]] = counts.get(r[3], 0) + 1
    print("\n=== 汇总 ===")
    for k, v in counts.items():
        print(f"  {k}: {v}")
    with open("/tmp/3_status.tsv", "w", encoding="utf-8") as f:
        for idx, ln, code, status, note in results:
            f.write(f"{status}\t{idx}\t{ln}\n")
    print(f"结果写入 /tmp/3_status.tsv （{len(results)} 条）")

if __name__ == "__main__":
    main()
