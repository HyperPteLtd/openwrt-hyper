import { render } from "preact";
import { Root } from "./root";
import { locale } from "./lib/i18n";

document.documentElement.lang = locale;
render(<Root />, document.getElementById("root")!);
