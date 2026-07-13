interface SectionHeadingProps {
  index: string;
  title: string;
  description: string;
}

export function SectionHeading({ index, title, description }: SectionHeadingProps) {
  return (
    <header className="section-heading">
      <span>{index}</span>
      <div>
        <h2>{title}</h2>
        <p>{description}</p>
      </div>
    </header>
  );
}
