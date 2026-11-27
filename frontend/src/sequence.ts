/**
 * Sequence 是「只有最新一次请求的结果可以落地」的计数器。
 *
 * 会话代次与同一资源的请求代次都用它：发起请求前 next() 领一个号，响应回来时
 * stale() 判断自己是否已经被更新的请求取代。会话代次挡换登录前后的在途请求，
 * 资源代次挡同一会话里逆序完成的列表请求——轮询、手工刷新与新建设备后的刷新
 * 都会同时发列表请求，先发的晚到就会把新结果盖回旧数据。
 */
export class Sequence {
  private latest = 0;

  /** next 领一个新号，之前领到的号全部作废。 */
  next(): number {
    this.latest += 1;
    return this.latest;
  }

  /** current 是最近发出的那一号，供还没领号的调用方使用。 */
  current(): number {
    return this.latest;
  }

  /** stale 报告 claim 是否已经被更新的请求取代。 */
  stale(claim: number): boolean {
    return claim !== this.latest;
  }
}
