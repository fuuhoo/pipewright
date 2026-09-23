"""分组权限真机测试矩阵 —— 打运行中的实例(真库真 HTTP,非 mock)。

用法:
  python3 scripts/perm-matrix.py              # 建夹具 + 跑 79 条断言 + 保留夹具
  python3 scripts/perm-matrix.py teardown     # 清夹具(资源移出未归组 + 删 QA-* 组 + 停用 qa_* 账号)

口令与地址走环境变量,默认值是**本机测试实例**专用,别拿去连真实环境:
  PERM_BASE_URL(实例地址)· PERM_ADMIN_PASSWORD(管理员口令)· PERM_QA_PASSWORD(测试账号口令)

结果 JSON 写到 /tmp/permqa-results.json,供 docs/分组权限测试报告.md 对账。
"""
import http.cookiejar
import json
import os
import sys
import time
import urllib.error
import urllib.request

B = os.environ.get('PERM_BASE_URL', 'http://localhost:8080')
PW = os.environ.get('PERM_QA_PASSWORD', 'qa-perm-2026')
ADMIN_PW = os.environ.get('PERM_ADMIN_PASSWORD', 'admin123')
RESULTS = []
SETUP = []


class Client:
    def __init__(self, user, pw):
        self.jar = http.cookiejar.CookieJar()
        self.op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))
        self.user = user
        code, body = self.call('POST', '/api/auth/login', {'username': user, 'password': pw})
        if code != 200:
            raise RuntimeError(f'登录失败 {user}: {code} {body}')

    def call(self, method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(B + path, method=method, data=data)
        csrf = next((c.value for c in self.jar if 'csrf' in c.name.lower()), '')
        if csrf:
            req.add_header('X-CSRF-Token', csrf)
        if data:
            req.add_header('Content-Type', 'application/json')
        try:
            r = self.op.open(req)
            code, raw = r.status, r.read().decode()
        except urllib.error.HTTPError as e:
            code, raw = e.code, e.read().decode()
        except Exception as e:
            return str(e), None
        try:
            return code, json.loads(raw) if raw else None
        except Exception:
            return code, raw


def anon():
    c = Client.__new__(Client)
    c.jar = http.cookiejar.CookieJar()
    c.op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(c.jar))
    c.user = '匿名'
    return c


def rows(body):
    if isinstance(body, dict):
        for k in ('items', 'runs', 'projects', 'servers', 'entries'):
            if isinstance(body.get(k), list):
                return body[k]
        return []
    return body if isinstance(body, list) else []


def api_err(body):
    if isinstance(body, dict):
        return (body.get('error') or {}).get('code', '')
    return ''


def t(area, desc, actor, method, path, body=None, expect=None):
    code, payload = actor.call(method, path, body)
    if callable(expect):
        passed, detail = expect(code, payload)
        actual = f'{code} {detail}'
        exp = '谓词'
    else:
        passed = code == expect
        suffix = f' {api_err(payload)}' if api_err(payload) else ''
        actual = f'{code}{suffix}'
        exp = str(expect)
    RESULTS.append({'area': area, 'desc': desc, 'actor': actor.user, 'req': f'{method} {path}',
                    'expect': exp, 'actual': actual, 'pass': bool(passed)})
    return code, payload


def s(desc, ok, extra=''):
    SETUP.append({'desc': desc, 'pass': bool(ok), 'extra': str(extra)})


def srv_body(sv, group_id):
    keep = ('name', 'host', 'port', 'user', 'credentialId', 'description')
    out = {k: sv[k] for k in keep if k in sv}
    out['groupId'] = group_id
    return out


def ungroup_all(admin):
    for p in rows(admin.call('GET', '/api/projects')[1]):
        if p.get('groupId'):
            admin.call('PATCH', f"/api/projects/{p['id']}", {'groupId': ''})
    for sv in rows(admin.call('GET', '/api/servers')[1]):
        if sv.get('groupId'):
            admin.call('PUT', f"/api/servers/{sv['id']}", srv_body(sv, ''))


def first_run(admin, project_id, limit=1):
    if not project_id:
        return None
    _, body = admin.call('GET', f'/api/runs?projectId={project_id}&limit={limit}')
    rs = rows(body)
    return rs[0]['id'] if rs else None


def setup():
    admin = Client('admin', ADMIN_PW)
    ids = {u['username']: u['id'] for u in rows(admin.call('GET', '/api/admin/users?includeDisabled=1')[1])}
    for u in ('qa_lead', 'qa_mem', 'qa_out'):
        if u in ids:
            admin.call('PATCH', f"/api/admin/users/{ids[u]}", {'enabled': True, 'description': '分组权限测试账号'})
            admin.call('POST', f"/api/admin/users/{ids[u]}/password", {'password': PW})
            continue
        code, _ = admin.call('POST', '/api/admin/users',
                             {'username': u, 'password': PW, 'role': 'user', 'description': '分组权限测试账号'})
        s(f'建普通账号 {u}', code == 201, code)
        ids = {u2['username']: u2['id'] for u2 in rows(admin.call('GET', '/api/admin/users?includeDisabled=1')[1])}
    if 'qa_admin2' not in ids:
        code, _ = admin.call('POST', '/api/admin/users',
                             {'username': 'qa_admin2', 'password': PW, 'role': 'admin', 'description': '分组权限测试账号(第二个管理员,用于自禁用用例)'})
        s('建管理员账号 qa_admin2', code == 201, code)
        ids = {u2['username']: u2['id'] for u2 in rows(admin.call('GET', '/api/admin/users?includeDisabled=1')[1])}
    else:
        admin.call('PATCH', f"/api/admin/users/{ids['qa_admin2']}", {'enabled': True})

    ungroup_all(admin)
    for g in rows(admin.call('GET', '/api/groups')[1]):
        if g['name'].startswith('QA-'):
            code, _ = admin.call('DELETE', f"/api/groups/{g['id']}")
            s(f'清理旧夹具组 {g["name"]}', code == 204, code)

    code, priv = admin.call('POST', '/api/groups', {
        'name': 'QA-私有组', 'visibility': 'private', 'description': '权限测试夹具',
        'ownerId': ids['qa_lead'], 'memberIds': [ids['qa_mem']]})
    s('建私有组 QA-私有组(组长 qa_lead / 成员 qa_mem)', code == 201, code)
    code, pub = admin.call('POST', '/api/groups', {'name': 'QA-公开组', 'visibility': 'public', 'description': '权限测试夹具'})
    s('建公开组 QA-公开组(组长缺省为 admin)', code == 201, code)

    projs = {p['name']: p for p in rows(admin.call('GET', '/api/projects')[1])}
    srvs = {x['name']: x for x in rows(admin.call('GET', '/api/servers')[1])}
    fx = {'priv': priv, 'pub': pub, 'ids': ids, 'projs': projs, 'srvs': srvs}

    if 'smoke-go' in projs:
        code, _ = admin.call('PATCH', f"/api/projects/{projs['smoke-go']['id']}", {'groupId': priv['id']})
        s('项目 smoke-go → QA-私有组', code == 200, code)
    if 'smoke-web' in projs:
        code, _ = admin.call('PATCH', f"/api/projects/{projs['smoke-web']['id']}", {'groupId': pub['id']})
        s('项目 smoke-web → QA-公开组', code == 200, code)
    if 'p3-in-group180594' in srvs:
        code, _ = admin.call('PUT', f"/api/servers/{srvs['p3-in-group180594']['id']}",
                             srv_body(srvs['p3-in-group180594'], priv['id']))
        s('主机 p3-in-group180594 → QA-私有组', code == 200, code)
    s('主机 p3-free180594 保持未归组', 'p3-free180594' in srvs)
    s('项目 smoke-java 保持未归组', 'smoke-java' in projs)

    fx['run_priv'] = first_run(admin, projs.get('smoke-go', {}).get('id'))
    fx['run_pub'] = first_run(admin, projs.get('smoke-web', {}).get('id'))
    fx['run_free'] = first_run(admin, projs.get('smoke-java', {}).get('id'))
    s('取到私有组项目的历史 run', bool(fx['run_priv']))
    s('取到公开组项目的历史 run', bool(fx['run_pub']))
    return admin, fx


def has(names):
    """谓词工厂:列表里出现任一名字 → 失败。"""
    def f(code, body):
        got = {r.get('name') or r.get('id') for r in rows(body)}
        hit = sorted(set(names) & got)
        return (code == 200 and not hit), f'含 {hit}' if hit else '不含'
    return f


def hasnt(names):
    def f(code, body):
        got = {r.get('name') or r.get('id') for r in rows(body)}
        hit = sorted(set(names) & got)
        return (code == 200 and len(hit) == len(set(names))), f'缺 {sorted(set(names) - got)}' if len(hit) != len(set(names)) else '齐全'
    return f


def contains_id(rid):
    def f(code, body):
        got = {r.get('id') for r in rows(body)}
        return (code == 200 and rid not in got), '含该 id' if rid in got else '不含该 id'
    return f


def matrix(admin, fx):
    lead = Client('qa_lead', PW)
    mem = Client('qa_mem', PW)
    out = Client('qa_out', PW)
    admin2 = Client('qa_admin2', PW)
    priv, pub, ids = fx['priv'], fx['pub'], fx['ids']
    go, web, java = fx['projs'].get('smoke-go'), fx['projs'].get('smoke-web'), fx['projs'].get('smoke-java')
    in_srv, free_srv = fx['srvs'].get('p3-in-group180594'), fx['srvs'].get('p3-free180594')

    # ── A 账号与角色 ──
    A = 'A 账号与角色'
    newname = 'qa_t' + time.strftime('%H%M%S')
    code, tmp = t(A, '管理员建号', admin, 'POST', '/api/admin/users',
                  {'username': newname, 'password': PW, 'role': 'user'}, expect=201)
    t(A, '普通用户不能建号(RequireAdmin)', out, 'POST', '/api/admin/users',
      {'username': 'qa_x', 'password': PW, 'role': 'user'}, expect=403)
    t(A, '建号弱口令被拒 422', admin, 'POST', '/api/admin/users',
      {'username': 'qa_weak', 'password': '123', 'role': 'user'}, expect=422)
    t(A, '内置管理员行不可在此禁用(409)', admin, 'PATCH',
      '/api/admin/users/' + '00000000-0000-0000-0000-000000000001', {'enabled': False}, expect=409)
    t(A, '管理员不能禁用自己(409 self_disable)', admin2, 'PATCH', f"/api/admin/users/{ids['qa_admin2']}",
      {'enabled': False}, expect=409)
    if code == 201:
        admin.call('PATCH', f"/api/admin/users/{tmp['id']}", {'enabled': False})
        c2, _ = anon().call('POST', '/api/auth/login', {'username': newname, 'password': PW})
        RESULTS.append({'area': A, 'desc': '被禁用账号无法登录', 'actor': '匿名', 'req': 'POST /api/auth/login',
                        'expect': '401', 'actual': str(c2), 'pass': c2 == 401})
    t(A, '未登录访问项目列表 → 401', anon(), 'GET', '/api/projects', expect=401)

    # ── B 分组管理 ──
    Bg = 'B 分组管理'
    t(Bg, '普通用户不能建组(仅管理员)', out, 'POST', '/api/groups',
      {'name': 'QA-不该存在', 'visibility': 'private'}, expect=403)
    t(Bg, '组长可改组属性(这里改描述)', lead, 'PATCH', f"/api/groups/{priv['id']}", {'description': '组长改过'}, expect=200)
    t(Bg, '成员不能改组', mem, 'PATCH', f"/api/groups/{priv['id']}", {'description': 'x'}, expect=403)
    t(Bg, '局外人不能改组', out, 'PATCH', f"/api/groups/{priv['id']}", {'description': 'x'}, expect=403)
    t(Bg, '组长可加成员', lead, 'POST', f"/api/groups/{priv['id']}/members", {'userId': ids['qa_out']}, expect=200)
    t(Bg, '组长可移除成员', lead, 'DELETE', f"/api/groups/{priv['id']}/members/{ids['qa_out']}", expect=200)
    t(Bg, '成员不能改名册', mem, 'POST', f"/api/groups/{priv['id']}/members", {'userId': ids['qa_out']}, expect=403)
    t(Bg, '组长不能更换组长(仅管理员)', lead, 'PATCH', f"/api/groups/{priv['id']}", {'ownerId': ids['qa_mem']}, expect=403)
    t(Bg, '管理员看私有组详情', admin, 'GET', f"/api/groups/{priv['id']}", expect=200)
    t(Bg, '成员看私有组详情', mem, 'GET', f"/api/groups/{priv['id']}", expect=200)
    t(Bg, '局外人看私有组详情 → 403(不伪装 404)', out, 'GET', f"/api/groups/{priv['id']}", expect=403)

    def only_public(code, body):
        names = [g['name'] for g in rows(body)]
        return (code == 200 and 'QA-私有组' not in names and 'QA-公开组' in names), str(names)

    def both(code, body):
        names = [g['name'] for g in rows(body)]
        return (code == 200 and 'QA-私有组' in names and 'QA-公开组' in names), str(names)

    t(Bg, '局外人的组列表只含公开组', out, 'GET', '/api/groups', expect=only_public)
    t(Bg, '成员的组列表含自己所在私有组 + 公开组', mem, 'GET', '/api/groups', expect=both)

    def manage_flags(code, body):
        m = {g['name']: g['canManage'] for g in rows(body)}
        return (m.get('QA-私有组') is True and m.get('QA-公开组') is False), str(m)

    t(Bg, 'canManage 由服务端判定(组长:私有 true / 公开 false)', lead, 'GET', '/api/groups', expect=manage_flags)
    t(Bg, '组长不能删组(仅管理员)', lead, 'DELETE', f"/api/groups/{priv['id']}", expect=403)
    t(Bg, '管理员删非空组被拒 409(先移空)', admin, 'DELETE', f"/api/groups/{priv['id']}", expect=409)

    # ── C 项目 ──
    C = 'C 项目'
    if go:
        t(C, '局外人项目列表不含私有组项目', out, 'GET', '/api/projects', expect=has([go['name']]))
        t(C, '成员项目列表含私有组项目', mem, 'GET', '/api/projects', expect=hasnt([go['name']]))
        t(C, '局外人读私有组项目流水线 → 403', out, 'GET', f"/api/projects/{go['id']}/pipeline", expect=403)
        t(C, '成员读私有组项目流水线 → 200', mem, 'GET', f"/api/projects/{go['id']}/pipeline", expect=200)
        t(C, '组长读私有组项目流水线 → 200', lead, 'GET', f"/api/projects/{go['id']}/pipeline", expect=200)
        t(C, '局外人改私有组项目 → 403', out, 'PATCH', f"/api/projects/{go['id']}", {'description': 'x'}, expect=403)
        t(C, '局外人删私有组项目 → 403', out, 'DELETE', f"/api/projects/{go['id']}", expect=403)
        t(C, '成员可改组内项目(Operate)', mem, 'PATCH', f"/api/projects/{go['id']}", {'description': go.get('description', '')}, expect=200)
        t(C, '成员不能改归属(Manage 需组长/管理员)', mem, 'PATCH', f"/api/projects/{go['id']}", {'groupId': pub['id']}, expect=403)
        t(C, '组长不能把组内项目挪进自己不管的组(公开组归 admin)', lead, 'PATCH', f"/api/projects/{go['id']}", {'groupId': pub['id']}, expect=403)
    if web:
        t(C, '局外人可读公开组项目流水线', out, 'GET', f"/api/projects/{web['id']}/pipeline", expect=200)
        t(C, '公开组项目改归属仍仅管理员(局外人 403)', out, 'PATCH', f"/api/projects/{web['id']}", {'groupId': ''}, expect=403)
    if java:
        t(C, '未归组项目对局外人可读', out, 'GET', f"/api/projects/{java['id']}/pipeline", expect=200)
        t(C, '未归组项目对局外人可改(Operate 全员)', out, 'PATCH', f"/api/projects/{java['id']}",
          {'description': java.get('description', '')}, expect=200)
        t(C, '未归组项目的归属只有管理员能动(组长 403)', lead, 'PATCH', f"/api/projects/{java['id']}",
          {'groupId': priv['id']}, expect=403)

    # ── D 运行信息随项目走 ──
    D = 'D 运行'
    if go:
        t(D, '局外人按项目查 run 列表为空', out, 'GET', f"/api/runs?projectId={go['id']}&limit=50",
          expect=lambda c, b: (c == 200 and len(rows(b)) == 0, f'{len(rows(b))} 条'))
    if fx.get('run_priv'):
        t(D, '局外人全局 run 列表不含私有组 run', out, 'GET', '/api/runs?limit=200', expect=contains_id(fx['run_priv']))
        t(D, '局外人直连私有组 run → 403', out, 'GET', f"/api/runs/{fx['run_priv']}", expect=403)
        t(D, '局外人取私有组 run 日志 → 403', out, 'GET', f"/api/runs/{fx['run_priv']}/logs", expect=403)
        t(D, '成员取该 run 详情 → 200', mem, 'GET', f"/api/runs/{fx['run_priv']}", expect=200)
        t(D, '成员取该 run 日志 → 200', mem, 'GET', f"/api/runs/{fx['run_priv']}/logs", expect=200)
    if fx.get('run_pub'):
        t(D, '局外人可读公开组 run', out, 'GET', f"/api/runs/{fx['run_pub']}", expect=200)
    if fx.get('run_free'):
        t(D, '局外人可读未归组项目 run', out, 'GET', f"/api/runs/{fx['run_free']}", expect=200)

    # ── E 主机 / 容器 ──
    E = 'E 主机与容器'
    if in_srv:
        t(E, '局外人主机列表不含私有组主机', out, 'GET', '/api/servers', expect=has([in_srv['name']]))
        t(E, '成员主机列表含私有组主机', mem, 'GET', '/api/servers', expect=hasnt([in_srv['name']]))
        t(E, '局外人直连私有组主机 → 403', out, 'GET', f"/api/servers/{in_srv['id']}", expect=403)
        t(E, '成员直连私有组主机 → 200', mem, 'GET', f"/api/servers/{in_srv['id']}", expect=200)
        t(E, '批量指标总览不含私有组主机', out, 'GET', '/api/servers/metrics', expect=has([in_srv['name']]))
        t(E, '批量容器总览不含私有组主机', out, 'GET', '/api/servers/containers', expect=has([in_srv['name']]))
        t(E, '局外人探测私有组主机 → 403', out, 'POST', f"/api/servers/{in_srv['id']}/test", {}, expect=403)
        t(E, '成员不能改主机归属', mem, 'PUT', f"/api/servers/{in_srv['id']}", srv_body(in_srv, pub['id']), expect=403)
        t(E, '组长也不能把主机挪到自己不管的组(公开组归 admin)', lead, 'PUT',
          f"/api/servers/{in_srv['id']}", srv_body(in_srv, pub['id']), expect=403)
    t(E, '普通用户登记未归组主机 → 403(仅管理员)', out, 'POST', '/api/servers',
      {'name': 'QA-局外机', 'host': '10.0.0.9', 'port': 22, 'user': 'root'}, expect=403)
    if in_srv:
        t(E, '普通用户登记到别人私有组 → 403', out, 'POST', '/api/servers',
          {'name': 'QA-局外机2', 'host': '10.0.0.9', 'port': 22, 'user': 'root', 'groupId': priv['id']}, expect=403)
        code, created = t(E, '组长可登记主机进自己的组', lead, 'POST', '/api/servers',
                          {'name': 'QA-组长机', 'host': '10.0.0.9', 'port': 22, 'user': 'root',
                           'credentialId': in_srv.get('credentialId', ''), 'groupId': priv['id']}, expect=201)
        if code == 201:
            t(E, '清理:组长删自己刚建的主机', lead, 'DELETE', f"/api/servers/{created['id']}", expect=204)
    if free_srv:
        t(E, '未归组主机对局外人可读', out, 'GET', f"/api/servers/{free_srv['id']}", expect=200)
        t(E, '未归组主机的归属只有管理员能动(组长 403)', lead, 'PUT',
          f"/api/servers/{free_srv['id']}", srv_body(free_srv, priv['id']), expect=403)

    # ── F 凭据分级 ──
    F = 'F 凭据分级'
    code, cred = t(F, '普通用户可建自己的 personal 凭据', mem, 'POST', '/api/credentials',
                   {'name': 'qa-mem-token', 'type': 'git_token', 'scope': 'personal',
                    'username': 'mem', 'secret': 'qa-not-a-real-secret'}, expect=201)
    if code == 201:
        def mine_absent(c, b):
            names = [x['name'] for x in rows(b)]
            return ('qa-mem-token' not in names), str(names)

        def mine_present(c, b):
            names = [x['name'] for x in rows(b)]
            return ('qa-mem-token' in names), str(names)

        t(F, '局外人凭据列表看不到他人 personal', out, 'GET', '/api/credentials', expect=mine_absent)
        t(F, '管理员列表看得到(视图收敛不等于数据丢失)', admin, 'GET', '/api/credentials', expect=mine_present)
        t(F, '本人也不能 reveal 明文(本期收紧:仅管理员)', mem, 'POST', f"/api/credentials/{cred['id']}/reveal", {}, expect=403)
        t(F, '局外人 reveal → 403', out, 'POST', f"/api/credentials/{cred['id']}/reveal", {}, expect=403)
        t(F, '管理员 reveal → 200(每次留审计)', admin, 'POST', f"/api/credentials/{cred['id']}/reveal", {}, expect=200)
        t(F, '局外人删他人凭据 → 403', out, 'DELETE', f"/api/credentials/{cred['id']}", expect=403)
        t(F, '本人删自己的 personal 凭据', mem, 'DELETE', f"/api/credentials/{cred['id']}", expect=204)

    # ── G 平台设置类只开放给管理员 ──
    G = 'G 平台设置类'
    t(G, '普通用户读用户管理 → 403', out, 'GET', '/api/admin/users', expect=403)
    t(G, '普通用户读审计日志 → 403', out, 'GET', '/api/audit?limit=5', expect=403)
    t(G, '管理员读审计日志 → 200', admin, 'GET', '/api/audit?limit=5', expect=200)
    t(G, '普通用户读管理端构建环境 → 403', out, 'GET', '/api/admin/build-envs', expect=403)
    t(G, '普通用户可读预置构建环境目录(白名单)', out, 'GET', '/api/build-envs', expect=200)
    t(G, '普通用户可读预置配置资源目录', out, 'GET', '/api/config-profiles', expect=200)
    t(G, '普通用户写构建环境 → 403', out, 'POST', '/api/admin/build-envs', {'name': 'x'}, expect=403)

    # ── H 补充:触发运行 / 审计留痕 / 会话撤销取舍 ──
    H = 'H 补充'
    if go:
        t(H, '局外人触发私有组项目的运行 → 403(不只是看不见)', out, 'POST',
          f"/api/projects/{go['id']}/runs", {'branch': 'master'}, expect=403)

    def audit_has(actions):
        def f(code, body):
            got = {r.get('action') for r in rows(body)}
            miss = sorted(set(actions) - got)
            return (code == 200 and not miss), ('缺 ' + str(miss)) if miss else '齐全'
        return f

    t(H, '审计留痕:建组 / 归组 / reveal 都有记录', admin, 'GET', '/api/audit?limit=200',
      expect=audit_has({'group_create', 'project_reassign', 'credential_reveal'}))

    # 已知取舍:禁用只挡新登录,已签发的会话不撤销 —— 显式记一条,别让人以为禁了立刻失效。
    admin.call('PATCH', f"/api/admin/users/{ids['qa_out']}", {'enabled': False})
    code, _ = out.call('GET', '/api/projects')
    RESULTS.append({'area': H, 'desc': '已知取舍:禁用账号后旧会话仍可用(不撤销)', 'actor': 'qa_out(已禁用)',
                    'req': 'GET /api/projects', 'expect': '200(现状,非期望)', 'actual': str(code),
                    'pass': code == 200})
    admin.call('PATCH', f"/api/admin/users/{ids['qa_out']}", {'enabled': True})


def report(admin, fx):
    total = len(RESULTS)
    bad = [r for r in RESULTS if not r['pass']]
    by_area = {}
    for r in RESULTS:
        a = by_area.setdefault(r['area'], {'n': 0, 'ok': 0})
        a['n'] += 1
        a['ok'] += 1 if r['pass'] else 0
    print(f'\n夹具 {sum(1 for x in SETUP if x["pass"])}/{len(SETUP)} 项就绪')
    for x in SETUP:
        print(('  ok  ' if x['pass'] else '  !!  ') + x['desc'] + (f'  ({x["extra"]})' if x['extra'] and not x['pass'] else ''))
    print(f'\n断言 {total - len(bad)}/{total} 通过')
    for area, v in by_area.items():
        print(f'  {area:12s} {v["ok"]}/{v["n"]}')
    if bad:
        print('\n失败:')
        for r in bad:
            print(f'  [{r["area"]}] {r["desc"]} / {r["actor"]} {r["req"]} 期望 {r["expect"]} 实际 {r["actual"]}')
    json.dump({'setup': SETUP, 'cases': RESULTS}, open('/tmp/permqa-results.json', 'w'),
              ensure_ascii=False, indent=1)
    print('\n结果已写 /tmp/permqa-results.json')
    return len(bad)


def main():
    if len(sys.argv) > 1 and sys.argv[1] == 'teardown':
        admin = Client('admin', ADMIN_PW)
        ungroup_all(admin)
        for g in rows(admin.call('GET', '/api/groups')[1]):
            if g['name'].startswith('QA-'):
                admin.call('DELETE', f"/api/groups/{g['id']}")
        ids = {u['username']: u['id'] for u in rows(admin.call('GET', '/api/admin/users?includeDisabled=1')[1])}
        for name, uid in ids.items():
            if name.startswith('qa_'):
                admin.call('PATCH', f"/api/admin/users/{uid}", {'enabled': False})
        for c in rows(admin.call('GET', '/api/credentials')[1]):
            if c['name'] == 'qa-mem-token':
                admin.call('DELETE', f"/api/credentials/{c['id']}")
        print('夹具已清理')
        return
    admin, fx = setup()
    matrix(admin, fx)
    report(admin, fx)


if __name__ == '__main__':
    main()
