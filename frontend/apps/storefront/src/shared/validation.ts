const whitespaceEdges = /^\p{White_Space}+|\p{White_Space}+$/gu;

// Go strings.TrimSpace uses Unicode White_Space; JS trim treats BOM differently.
export function trimDisplayName(value: string): string {
  return value.replace(whitespaceEdges, '');
}

export function wellFormed(value: string): boolean {
  return !Array.from(value).some((char) => {
    const point = char.codePointAt(0) ?? 0;
    return point >= 0xd800 && point <= 0xdfff;
  });
}
