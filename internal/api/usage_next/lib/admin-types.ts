export type User = {
  id: string;
  name: string;
  email: string;
  age?: number | null;
  gender?: string;
  country?: string;
  createdAt: string;
  updatedAt: string;
  emailVerified: boolean;
  banned: boolean;
  bannedAt?: string | null;
  lastSeenAt?: string | null;
  latestActivityAt?: string | null;
  lastIpAddress?: string | null;
  platform: string;
  books: number;
  writings: number;
  files: number;
  errorReports: number;
};

export type Activity = {
  method: string;
  path: string;
  status: number;
  platform: string;
  ipAddress?: string;
  createdAt: string;
};

export type UserDetails = Omit<User, "files"> & {
  files?: { filename: string; kind: string; contentType: string; sizeBytes: number; createdAt: string }[];
  records?: { collection: string; id: string; data: Record<string, unknown>; updatedAt: string }[];
  activities?: Activity[];
};

export type ErrorItem = {
  id: string;
  userId: string;
  userName: string;
  userEmail: string;
  message: string;
  title: string;
  details: string;
  steps: string;
  metadata: Record<string, unknown>;
  attachments: ErrorAttachment[];
  screen: string;
  platform: string;
  createdAt: string;
  resolvedAt?: string | null;
};

export type ErrorAttachment = {
  id: string;
  filename: string;
  contentType: string;
  sizeBytes: number;
};

export type Insight = {
  id: string;
  userId: string;
  userName: string;
  userEmail: string;
  category: string;
  message: string;
  status: string;
  createdAt: string;
};

export type PublicBook = {
  id: string;
  title: string;
  author: string;
  category: string;
  totalPages: number;
  fileName?: string;
};

export type ChartPoint = { date: string; count: number };
export type BreakdownPoint = { label: string; platform?: string; count: number };

export type Dashboard = {
  health: {
    status: string;
    uptimeSeconds: number;
    database: string;
    databaseSizeBytes: number;
    databaseConnections: number;
    databaseIdleConnections: number;
    goroutines: number;
    memoryBytes: number;
    checkedAt: string;
    platform: string;
    goVersion: string;
  };
  counts: {
    users: number;
    books: number;
    publicBooks: number;
    unresolvedErrors: number;
    requests24h: number;
  };
  charts: {
    usersByDay: ChartPoint[];
    requestsByDay: ChartPoint[];
    platforms: BreakdownPoint[];
    requestsByActor: BreakdownPoint[];
    responseStatuses: BreakdownPoint[];
    usersByCountry: BreakdownPoint[];
    usersByAge: BreakdownPoint[];
    usersByGender: BreakdownPoint[];
  };
};

export type RequestItem = {
  id: number;
  method: string;
  path: string;
  status: number;
  durationMs: number;
  createdAt: string;
  actorType: "admin" | "user" | "anonymous";
  actorId: string;
  ipAddress: string;
  userAgent: string;
  requestBytes: number;
  responseBytes: number;
};

export type UsagePoint = {
  period: string;
  requests: number;
  newUsers: number;
  errors: number;
  avgDurationMs: number;
  maxDurationMs: number;
  peakMemoryBytes: number;
  peakGoroutines: number;
  peakDatabaseConnections: number;
  peakDatabaseSizeBytes: number;
  peakUptimeSeconds: number;
};

export type UsageSummary = {
  label: string;
  requests: number;
  success: number;
  redirects: number;
  clientErrors: number;
  serverErrors: number;
  errors: number;
  newUsers: number;
  avgDurationMs: number;
  maxDurationMs: number;
};

export type UsageAnalytics = {
  period: "day" | "month" | "year";
  series: UsagePoint[];
  comparison: {
    current: UsageSummary;
    previous: UsageSummary;
    requestChangePercent: number;
    errorChangePercent: number;
  };
  app: { platform: string; goVersion: string; uptimeSeconds: number };
};

export type AdminProfile = {
  username: string;
  email: string;
  avatarDataUrl: string;
  updatedAt: string;
  platform: string;
  serverPlatform: string;
  ipAddress: string;
};

export type AdminNote = {
  id: string;
  title: string;
  content: string;
  createdAt: string;
  updatedAt: string;
};

export type AdminAlert = {
  id: string;
  category: "error" | "insight" | "server_error";
  title: string;
  details: string;
  createdAt: string;
};

export type PageKey = "overview" | "analytics" | "users" | "insights" | "requests" | "library" | "notifications" | "notes" | "profile";
