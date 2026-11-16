import { locale } from "./i18n.ts";
/** ISO region aliases from Unicode CLDR codeMappings (see UNICODE-LICENSE.txt). */
const ALPHA3: Record<string, string> = Object.fromEntries(
  "AAA:AA ASC:AC AND:AD ARE:AE AFG:AF ATG:AG AIA:AI ALB:AL ARM:AM ANT:AN AGO:AO ATA:AQ ARG:AR ASM:AS AUT:AT AUS:AU ABW:AW ALA:AX AZE:AZ BIH:BA BRB:BB BGD:BD BEL:BE BFA:BF BGR:BG BHR:BH BDI:BI BEN:BJ BLM:BL BMU:BM BRN:BN BOL:BO BES:BQ BRA:BR BHS:BS BTN:BT BUR:BU BVT:BV BWA:BW BLR:BY BLZ:BZ CAN:CA CCK:CC COD:CD CAF:CF COG:CG CHE:CH CIV:CI COK:CK CHL:CL CMR:CM CHN:CN COL:CO CPT:CP CRI:CR SCG:CS CUB:CU CPV:CV CUW:CW CXR:CX CYP:CY CZE:CZ DDR:DD DEU:DE DGA:DG DJI:DJ DNK:DK DMA:DM DOM:DO DZA:DZ ECU:EC EST:EE EGY:EG ESH:EH ERI:ER ESP:ES ETH:ET QUU:EU FIN:FI FJI:FJ FLK:FK FSM:FM FRO:FO FRA:FR FXX:FX GAB:GA GBR:GB GRD:GD GEO:GE GUF:GF GGY:GG GHA:GH GIB:GI GRL:GL GMB:GM GIN:GN GLP:GP GNQ:GQ GRC:GR SGS:GS GTM:GT GUM:GU GNB:GW GUY:GY HKG:HK HMD:HM HND:HN HRV:HR HTI:HT HUN:HU IDN:ID IRL:IE ISR:IL IMN:IM IND:IN IOT:IO IRQ:IQ IRN:IR ISL:IS ITA:IT JEY:JE JAM:JM JOR:JO JPN:JP KEN:KE KGZ:KG KHM:KH KIR:KI COM:KM KNA:KN PRK:KP KOR:KR KWT:KW CYM:KY KAZ:KZ LAO:LA LBN:LB LCA:LC LIE:LI LKA:LK LBR:LR LSO:LS LTU:LT LUX:LU LVA:LV LBY:LY MAR:MA MCO:MC MDA:MD MNE:ME MAF:MF MDG:MG MHL:MH MKD:MK MLI:ML MMR:MM MNG:MN MAC:MO MNP:MP MTQ:MQ MRT:MR MSR:MS MLT:MT MUS:MU MDV:MV MWI:MW MEX:MX MYS:MY MOZ:MZ NAM:NA NCL:NC NER:NE NFK:NF NGA:NG NIC:NI NLD:NL NOR:NO NPL:NP NRU:NR NTZ:NT NIU:NU NZL:NZ OMN:OM PAN:PA PER:PE PYF:PF PNG:PG PHL:PH PAK:PK POL:PL SPM:PM PCN:PN PRI:PR PSE:PS PRT:PT PLW:PW PRY:PY QAT:QA QMM:QM QNN:QN QOO:QO QPP:QP QQQ:QQ QRR:QR QSS:QS QTT:QT QUU:QU QVV:QV QWW:QW QXX:QX QYY:QY QZZ:QZ REU:RE ROU:RO SRB:RS RUS:RU RWA:RW SAU:SA SLB:SB SYC:SC SDN:SD SWE:SE SGP:SG SHN:SH SVN:SI SJM:SJ SVK:SK SLE:SL SMR:SM SEN:SN SOM:SO SUR:SR SSD:SS STP:ST SUN:SU SLV:SV SXM:SX SYR:SY SWZ:SZ TAA:TA TCA:TC TCD:TD ATF:TF TGO:TG THA:TH TJK:TJ TKL:TK TLS:TL TKM:TM TUN:TN TON:TO TMP:TP TUR:TR TTO:TT TUV:TV TWN:TW TZA:TZ UKR:UA UGA:UG UMI:UM USA:US URY:UY UZB:UZ VAT:VA VCT:VC VEN:VE VGB:VG VIR:VI VNM:VN VUT:VU WLF:WF WSM:WS XAA:XA XBB:XB XCC:XC XDD:XD XEE:XE XFF:XF XGG:XG XHH:XH XII:XI XJJ:XJ XKK:XK XLL:XL XMM:XM XNN:XN XOO:XO XPP:XP XQQ:XQ XRR:XR XSS:XS XTT:XT XUU:XU XVV:XV XWW:XW XXX:XX XYY:XY XZZ:XZ YMD:YD YEM:YE MYT:YT YUG:YU ZAF:ZA ZMB:ZM ZAR:ZR ZWE:ZW ZZZ:ZZ".split(" ").map((pair) => pair.split(":")),
);

const names = new Intl.DisplayNames([locale], { type: "region", fallback: "none" });
export function regionCode(value?: string | null): string | null {
  const raw = value?.trim().toUpperCase() || "";
  const code = ALPHA3[raw] || (raw === "UK" ? "GB" : raw);
  return /^[A-Z]{2}$/.test(code) && code !== "ZZ" && names.of(code) ? code : null;
}
export function regionName(value?: string | null): string {
  const code = regionCode(value);
  return code ? names.of(code) || "" : "";
}
export function regionFlag(value?: string | null): string {
  const code = regionCode(value);
  return code ? String.fromCodePoint(...[...code].map((c) => c.charCodeAt(0) + 127397)) : "";
}
/** Translate standalone ISO codes without changing custom names or node IDs. */
export function nodeLabel(label: string): string {
  return label.replace(/\b[A-Z]{2,3}\b/g, (code) => regionName(code) || code);
}
export function nodeRegion(node: { label: string; country?: string }): string | null {
  return regionCode(node.country) || regionCode(node.label.match(/^([A-Za-z]{2,3})(?=[\s\d_-]|$)/)?.[1]);
}
