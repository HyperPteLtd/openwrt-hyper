import { t as tr } from "../lib/i18n";
import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import { app } from "../lib/app";
import { api, formatDate, type ApiResult } from "../lib/api";
import { productText } from "../lib/display";
import "./Announcements.css";

interface Announcement {
  id: number;
  title: string;
  content: string;
  created_at: number;
  pinned?: boolean;
}

/** A compact home feed; content stays plain text even when the server sends HTML. */
export function Announcements() {
  const notices = useSignal<Announcement[]>([]);
  const tick = app.tick;
  useEffect(() => {
    let live = true;
    const apply = (r: ApiResult<{ announcements: Announcement[] }>) => {
      if (live && r.ok) notices.value = [...(r.data?.announcements ?? [])].sort((a, b) => Number(!!b.pinned) - Number(!!a.pinned) || b.created_at - a.created_at);
    };
    api<{ announcements: Announcement[] }>("GET", "/v1/public/announcement/list", { query: { page: 1, size: 30 }, onUpdate: apply })
      .then(apply).catch(() => { /* keep the last available announcements */ });
    return () => { live = false; };
  }, [tick]);
  if (!notices.value.length) return null;
  return (
    <section class="card announcements announcement-feed" aria-labelledby="announcements-title">
      <h2 id="announcements-title">{tr("公告")}</h2>
      <div class="announcement-list">
        {notices.value.map((notice) => (
          <details class="announcement" key={notice.id}>
            <summary>
              <span>{notice.pinned && <span class="announcement-pin">{tr("置顶 · ")}</span>}{productText(notice.title)}</span>
              <time class="muted small">{formatDate(notice.created_at)}</time>
            </summary>
            <p>{productText(notice.content)}</p>
          </details>
        ))}
      </div>
    </section>
  );
}
