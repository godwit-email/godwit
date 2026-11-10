import {
  RiCheckLine,
  RiCodeLine,
  RiDoubleQuotesL,
  RiEditLine,
  RiFileTextLine,
  RiListOrdered,
  RiListUnordered,
  RiMailLine,
  RiPriceTag3Line,
  RiSearchLine,
  RiShieldLine,
  RiSparkling2Line,
  RiTimeLine,
  RiTranslate2,
} from "@remixicon/react";

// 兼容尚未返回显示名称和图标的旧绑定。
export type SkillChoice = {
  name: string;
  description: string;
  displayName?: string;
  icon?: string;
};

export const skillLabel = (name: string, displayName?: string) =>
  displayName?.replace(/\p{White_Space}+/gu, " ").trim() ||
  name
    .split("-")
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ");

const icons = {
  sparkles: RiSparkling2Line,
  "file-text": RiFileTextLine,
  mail: RiMailLine,
  list: RiListUnordered,
  "list-ordered": RiListOrdered,
  check: RiCheckLine,
  time: RiTimeLine,
  languages: RiTranslate2,
  search: RiSearchLine,
  shield: RiShieldLine,
  tag: RiPriceTag3Line,
  code: RiCodeLine,
  "pen-line": RiEditLine,
  quote: RiDoubleQuotesL,
};

export function SkillIcon({ icon }: { icon?: string }) {
  // 只接受固定键，不把模型给出的 SVG、URL 或组件名作为图标。
  const key = icon && Object.hasOwn(icons, icon) ? (icon as keyof typeof icons) : "sparkles";
  const Icon = icons[key];
  return <Icon aria-hidden="true" data-skill-icon={key} className="size-4 shrink-0" />;
}
