import { http } from "./http";

// 登录响应结构
export interface LoginResponse {
  access_token: string;
  token_type: string;
  user_id: number;
  username: string;
  role: string;
}

// 当前用户信息
export interface UserInfo {
  user_id: number;
  username: string;
  role: string;
  permissions: string[];
}

// 账号密码登录
export async function login(
  username: string,
  password: string,
): Promise<LoginResponse> {
  const response = await http.post<LoginResponse>("/auth/login", {
    username,
    password,
  });
  return response.data;
}

// 获取当前登录用户信息
export async function fetchMe(): Promise<UserInfo> {
  const response = await http.get<UserInfo>("/auth/me");
  return response.data;
}
