import { useSignal } from "@preact/signals";
import { Logo } from "../components/TopBar";
import { Notice } from "../components/ui";
import { app } from "../lib/app";
import { call } from "../lib/rpc";
import type { Result } from "../lib/types";

/*
 * Same flow as the Hyper app: email (+ optional invite code) -> verification code
 * -> login. An unknown email is registered by the backend on the fly.
 * No Apple login on the router (OAuth callbacks to a LAN address are unreliable).
 */
export function Login() {
  const step = useSignal<"email" | "code">("email");
  const email = useSignal("");
  const invite = useSignal("");
  const code = useSignal("");
  const busy = useSignal(false);
  const message = useSignal("");
  const cooldown = useSignal(0);

  const sendCode = async () => {
    message.value = "";
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email.value.trim())) {
      message.value = "请输入正确的邮箱地址。";
      return;
    }
    busy.value = true;
    try {
      const r = await call<Result>("send_code", { email: email.value.trim() });
      if (r.result) {
        step.value = "code";
        cooldown.value = 60;
        const id = setInterval(() => {
          cooldown.value--;
          if (cooldown.value <= 0) clearInterval(id);
        }, 1000);
      } else {
        message.value = r.msg || (r.error === "api_base_not_set" ? "还没有设置服务地址，请先到“设置”里填写。" : r.error) || "验证码发送失败";
      }
    } catch (e) {
      message.value = (e as Error).message;
    }
    busy.value = false;
  };

  const login = async () => {
    message.value = "";
    if (!/^\d{4,8}$/.test(code.value.trim())) {
      message.value = "请输入邮件里的验证码。";
      return;
    }
    busy.value = true;
    try {
      const r = await call<Result>("login", { email: email.value.trim(), code: code.value.trim(), invite: invite.value.trim() });
      if (r.result) {
        code.value = "";
        app.tick++;
      } else {
        message.value = r.msg || r.error || "登录失败";
      }
    } catch (e) {
      message.value = (e as Error).message;
    }
    busy.value = false;
  };

  return (
    <main class="page" style={{ alignItems: "center" }}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          (step.value === "email" ? sendCode : login)();
        }}
        class="card"
        style={{ width: "100%", maxWidth: "440px", gap: "18px", padding: "28px" }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: "16px" }}>
          <Logo size={44} />
          <div style={{ display: "flex", flexDirection: "column", gap: "4px" }}>
            <h2 style={{ fontSize: "24px" }}>登录 Hyper</h2>
            <span class="muted">使用邮箱登录或注册</span>
          </div>
        </div>

        {step.value === "email" ? (
          <>
            <label class="field">
              邮箱
              <input class="input" type="email" autocomplete="email" required value={email.value} onInput={(e) => (email.value = e.currentTarget.value)} placeholder="输入邮箱地址" />
            </label>
            <label class="field">
              邀请码（选填）
              <input class="input" value={invite.value} onInput={(e) => (invite.value = e.currentTarget.value)} placeholder="输入邀请码" />
            </label>
          </>
        ) : (
          <>
            <div style={{ display: "flex", alignItems: "center", gap: "10px", minHeight: "48px", padding: "0 14px", borderRadius: "12px", background: "var(--card-2)" }}>
              <span style={{ flexGrow: 1 }}>{email.value}</span>
              <button type="button" class="btn ghost" style={{ minHeight: "40px", padding: "0 8px", color: "var(--accent)" }} onClick={() => { step.value = "email"; message.value = ""; }}>
                更换
              </button>
            </div>
            <label class="field">
              验证码
              <input class="input" inputMode="numeric" autocomplete="one-time-code" required value={code.value} onInput={(e) => (code.value = e.currentTarget.value)} placeholder="邮件里的验证码" />
              <span class="muted small">验证码已发送到你的邮箱。没有账号会自动注册。</span>
            </label>
          </>
        )}

        <Notice text={message.value} />

        <button type="submit" class="btn primary big" disabled={busy.value}>
          {busy.value ? "请稍候…" : step.value === "email" ? "下一步" : "登录"}
        </button>
        {step.value === "code" && (
          <button type="button" class="btn ghost" disabled={busy.value || cooldown.value > 0} onClick={() => sendCode()}>
            {cooldown.value > 0 ? `${cooldown.value} 秒后可重新发送` : "重新发送验证码"}
          </button>
        )}

        <p class="muted small" style={{ margin: 0 }}>
          服务地址：{app.status?.api_base || "未设置"} · 登录凭证只保存在这台路由器上，不会留在浏览器里。
        </p>
      </form>
    </main>
  );
}
