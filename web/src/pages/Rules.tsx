import { OverviewNetwork } from "../components/OverviewNetwork";
import "./Rules.css";

/** The user's own lists and the server's routing rules, edited in place with autosave. */
export function Rules() {
  return (
    <main class="page rules-page">
      <div class="rules-layout">
        <OverviewNetwork section="custom" />
        <OverviewNetwork section="rules" />
      </div>
    </main>
  );
}
