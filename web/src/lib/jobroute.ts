import { projectRest } from '$lib/projectroute';

/** The rest parameter of a job's page under its project, or of one of its tabs. */
export function jobRest(project: string, jobId: string, ...tab: string[]): string {
	return projectRest(project, 'jobs', jobId, ...tab);
}

/** The rest parameter of a run's page under its job's project. */
export function runRest(project: string, runId: string): string {
	return projectRest(project, 'runs', runId);
}

export const JOB_TABS = ['runs', 'definition', 'schedule', 'webhooks'] as const;
export type JobTab = (typeof JOB_TABS)[number];

/** The job tab a URL names: '' is the Run tab, null a tab that does not exist. */
export function jobTab(sub: string[]): '' | JobTab | null {
	if (sub.length === 0) return '';
	if (sub.length > 1) return null;
	return (JOB_TABS as readonly string[]).includes(sub[0]) ? (sub[0] as JobTab) : null;
}
