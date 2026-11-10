import { cleanup, render } from "@testing-library/react";
import { SkillIcon, skillLabel } from "./Skills";

afterEach(cleanup);

describe("Skill 显示信息", () => {
  it("显示名称优先，旧数据从稳定标识生成名称", () => {
    expect(skillLabel("email-briefing", "邮件解读")).toBe("邮件解读");
    expect(skillLabel("create-skill")).toBe("Create Skill");
    expect(skillLabel("email-briefing", " ")).toBe("Email Briefing");
    expect(skillLabel("email-briefing", "  邮件解读  ")).toBe("邮件解读");
    expect(skillLabel("email-briefing", "Mail\n\tReview")).toBe("Mail Review");
  });

  it.each([undefined, "", "unknown", "__proto__", "https://example.com/icon.svg", "<svg />"])(
    "缺失或非白名单图标回退为 sparkles：%s",
    (icon) => {
      const { container } = render(<SkillIcon icon={icon} />);
      expect(container.querySelector('[data-skill-icon="sparkles"]')).toBeInTheDocument();
      expect(container.querySelector("img, image, use, script")).toBeNull();
    },
  );

  it("已知图标使用对应的内置 SVG", () => {
    const { container } = render(<SkillIcon icon="file-text" />);
    expect(container.querySelector('svg[data-skill-icon="file-text"]')).toBeInTheDocument();
    expect(container.querySelector('[data-skill-icon="sparkles"]')).toBeNull();
  });
});
